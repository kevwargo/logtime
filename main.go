package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kevwargo/logtime/internal/tasks"
	"github.com/ncruces/go-strftime"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

const workerEnvVar = "_LOGTIME_WORKER"

func main() {
	var r runner

	cmd := &cobra.Command{
		Use: "logtime [flags] -- command [arg1 [arg2 ...]]",
		RunE: func(cmd *cobra.Command, args []string) error {
			r.cfg.Command = args

			return r.run()
		},
	}

	cmd.Flags().StringVarP(&r.cfg.File, "filename", "o", "", "Redirect output here")
	cmd.Flags().BoolVarP(&r.cfg.Append, "append", "a", false, "Append to file")
	cmd.Flags().BoolVar(&r.cfg.TeeFlag, "tee", false, "Duplicate logs to stdout in addition to writing to file")
	cmd.Flags().StringVarP(&r.cfg.Format, "format", "f", "[%Y-%m-%d %H:%M:%S.%L] ", "Prefix format")
	cmd.Flags().BoolVar(
		&r.cfg.Subreaper,
		"set-subreaper",
		false,
		"Set PR_SET_CHILD_SUBREAPER flag before calling subprocess",
	)

	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

type config struct {
	File      string
	Format    string
	Append    bool
	TeeFlag   bool
	Subreaper bool
	Command   []string
}

type runner struct {
	cfg      config
	mu       sync.Mutex
	out      *os.File
	tee      bool
	isWorker bool
	cmdPID   int
}

func (r *runner) run() error {
	r.isWorker = os.Getenv(workerEnvVar) != ""
	os.Unsetenv(workerEnvVar)

	if r.cfg.File != "" && !r.isWorker {
		return r.runForeground()
	}

	return r.runWorker()
}

// runForeground re-execs logtime as a background worker and waits for the
// command to exit (reported by the worker via its stdout).
func (r *runner) runForeground() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving own executable: %w", err)
	}

	workerCmd := exec.Command(self)
	workerCmd.Env = append(os.Environ(), workerEnvVar+"=1")
	workerCmd.Stderr = os.Stderr

	ipcWriter, err := workerCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating worker stdin pipe: %w", err)
	}

	ipcReader, err := workerCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating worker stdout pipe: %w", err)
	}

	if err := workerCmd.Start(); err != nil {
		return fmt.Errorf("starting worker: %w", err)
	}

	enc := json.NewEncoder(ipcWriter)
	if err := enc.Encode(r.cfg); err != nil {
		return fmt.Errorf("encoding config to json: %w", err)
	}
	ipcWriter.Close()

	// Read IPC messages from the worker.
	// Protocol:
	//   "started <pid>" - command has been started
	//   "exited <code>" - command has exited with given code
	exitCode := 0
	sc := bufio.NewScanner(ipcReader)
	for sc.Scan() {
		line := sc.Text()
		if code, ok := strings.CutPrefix(line, "exited "); ok {
			exitCode, _ = strconv.Atoi(code)
			break
		}
	}

	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading worker IPC: %w", err)
	}

	// We don't call workerCmd.Wait() — the worker continues in the background.
	// Once we exit, the worker gets reparented to PID 1.

	if exitCode != 0 {
		os.Exit(exitCode)
	}

	return nil
}

// runWorker is the actual logging process. It creates pipes, starts the
// command, reports status to the foreground via stdout, and continues
// reading pipes and reaping children until everything is done.
func (r *runner) runWorker() error {
	if r.isWorker {
		dec := json.NewDecoder(os.Stdin)
		if err := dec.Decode(&r.cfg); err != nil {
			return fmt.Errorf("decoding config from stdin: %w", err)
		}
	}

	// When --filename is specified, always enable subreaper (we're the
	// background worker responsible for all descendants).
	if r.cfg.File != "" {
		r.cfg.Subreaper = true
	}

	cmd := exec.Command(r.cfg.Command[0], r.cfg.Command[1:]...)

	outPipe, err := r.openPipe(&cmd.Stdout, "stdout")
	if err != nil {
		return err
	}
	errPipe, err := r.openPipe(&cmd.Stderr, "stderr")
	if err != nil {
		return err
	}

	if r.cfg.Subreaper {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
			return fmt.Errorf("setting subreaper flag: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %v: %w", r.cfg.Command, err)
	}

	r.cmdPID = cmd.Process.Pid

	// Notify foreground that the command started (only in worker mode).
	if r.isWorker {
		fmt.Fprintf(os.Stdout, "started %d\n", r.cmdPID)
	}

	tg := tasks.NewGroup(outPipe.runUntilEOF, errPipe.runUntilEOF)
	if r.cfg.Subreaper {
		tg.Add(r.waitPIDs)
	} else {
		tg.Add(cmd.Wait)
	}

	return tg.Run()
}

// waitPIDs reaps all children (direct and reparented) until none remain.
// It reports the command's exit to the foreground via stdout IPC.
func (r *runner) waitPIDs() error {
	var (
		status unix.WaitStatus
		wpid   int
		err    error
	)

	stdoutClosed := false

	for {
		wpid, err = unix.Wait4(-1, &status, 0, nil)
		if err != nil {
			break
		}

		if wpid == r.cmdPID {
			r.log("Entrypoint command exited: code:%d, signal:%s", status.ExitStatus(), status.Signal().String())
			if r.isWorker {
				// Notify foreground of command exit.
				fmt.Fprintf(os.Stdout, "exited %d\n", status.ExitStatus())
				// Redirect stdout to /dev/null to prevent SIGPIPE if
				// the foreground exits and breaks the IPC pipe.
				r.closeStdout()
				stdoutClosed = true
			}
		} else {
			r.log("Subcommand %d exited: code:%d, signal:%s", wpid, status.ExitStatus(), status.Signal().String())
		}
	}

	if !stdoutClosed && r.isWorker {
		r.closeStdout()
	}

	if err == unix.ECHILD {
		r.log("All children exited")
		err = nil
	}

	return err
}

// closeStdout redirects FD 1 to /dev/null to prevent SIGPIPE on accidental
// writes after the foreground process has exited.
func (r *runner) closeStdout() {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return
	}
	unix.Dup2(int(devnull.Fd()), 1)
	devnull.Close()
}

func (r *runner) openPipe(target *io.Writer, name string) (*pipe, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	*target = pw

	return &pipe{
		r:    r,
		name: name,
		pr:   pr,
		pw:   pw,
	}, nil
}

func (r *runner) openLog() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.out != nil {
		return nil
	}

	if r.cfg.File == "" {
		r.out = os.Stdout
		r.tee = false

		return nil
	}

	flags := os.O_WRONLY | os.O_CREATE
	if r.cfg.Append {
		flags |= os.O_APPEND
	}

	f, err := os.OpenFile(r.cfg.File, flags, 0o644)
	if err != nil {
		return fmt.Errorf("opening log output %q: %w", r.cfg.File, err)
	}

	r.out = f
	r.tee = r.cfg.TeeFlag

	return nil
}

func (r *runner) log(f string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := strftime.Format(r.cfg.Format, time.Now())
	args = append([]any{prefix}, args...)

	msg := fmt.Sprintf(f, args...)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}

	fmt.Fprint(r.out, msg)
	if r.tee {
		fmt.Print(msg)
	}
}

type pipe struct {
	r    *runner
	name string
	pr   *os.File
	pw   *os.File
}

func (p *pipe) runUntilEOF() error {
	if err := p.pw.Close(); err != nil {
		return fmt.Errorf("closing parent write pipe: %w", err)
	}

	if err := p.r.openLog(); err != nil {
		return err
	}
	p.r.log("starting %q pipe", p.name)

	sc := bufio.NewScanner(p.pr)
	for sc.Scan() {
		p.r.log("[%s] %s", p.name, sc.Text())
	}

	err := sc.Err()
	if err != nil {
		err = fmt.Errorf("in %q pipe: %w", p.name, err)
	}

	return err
}
