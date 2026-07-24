package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kevwargo/logtime/internal/tasks"
	"github.com/ncruces/go-strftime"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func main() {
	var r runner

	cmd := &cobra.Command{
		Use: "logtime [flags] -- command [arg1 [arg2 ...]]",
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.run(args)
		},
	}

	cmd.Flags().StringVarP(&r.File, "filename", "o", "", "Redirect output here")
	cmd.Flags().BoolVarP(&r.Append, "append", "a", false, "Append to file")
	cmd.Flags().BoolVar(&r.TeeFlag, "tee", false, "Duplicate logs to stdout in addition to writing to file")
	cmd.Flags().StringVarP(&r.Format, "format", "f", "%Y%m%d-%H%M%S.%L", "Redirect output here")
	cmd.Flags().BoolVar(
		&r.Subreaper,
		"set-subreaper",
		false,
		"Set PR_SET_CHILD_SUBREAPER flag before calling subprocess",
	)

	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

type runner struct {
	File      string
	Format    string
	Append    bool
	TeeFlag   bool
	Subreaper bool

	mu        sync.Mutex
	out       *os.File
	tee       bool
	subCmdPID int
}

func (r *runner) run(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)

	outPipe, err := r.openPipe(&cmd.Stdout, "stdout")
	if err != nil {
		return err
	}
	errPipe, err := r.openPipe(&cmd.Stderr, "stderr")
	if err != nil {
		return err
	}

	if r.Subreaper {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
			return fmt.Errorf("setting subreaper flag: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %v: %w", args, err)
	}

	r.subCmdPID = cmd.Process.Pid

	var tg tasks.TaskGroup
	tg.Add(outPipe.runUntilEOF)
	tg.Add(errPipe.runUntilEOF)

	if r.Subreaper {
		tg.Add(r.waitPIDs)
	} else {
		tg.Add(cmd.Wait)
	}

	return tg.Run()
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

	if r.File == "" {
		r.out = os.Stdout
		r.tee = false

		return nil
	}

	flags := os.O_WRONLY | os.O_CREATE
	if r.Append {
		flags |= os.O_APPEND
	}

	f, err := os.OpenFile(r.File, flags, 0o644)
	if err != nil {
		return fmt.Errorf("opening log output %q: %w", r.File, err)
	}

	r.out = f
	r.tee = r.TeeFlag

	return nil
}

func (r *runner) log(f string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !strings.HasSuffix(f, "\n") {
		f += "\n"
	}

	now := strftime.Format(r.Format, time.Now())
	args = append([]any{now}, args...)

	fmt.Fprintf(r.out, "%s: "+f, args...)
	if r.tee {
		fmt.Printf("%s: "+f, args...)
	}
}

func (r *runner) waitPIDs() error {
	var (
		status unix.WaitStatus
		wpid   int
		err    error
	)

	for {
		wpid, err = unix.Wait4(-1, &status, 0, nil)
		if err != nil {
			break
		}

		if wpid == r.subCmdPID {
			r.log("Subcommand exited: code:%d, signal:%s", status.ExitStatus(), status.Signal().String())
		} else {
			r.log("Child %d exited: code:%d, signal:%s", wpid, status.ExitStatus(), status.Signal().String())
		}
	}

	if err == unix.ECHILD {
		r.log("All children exited")
		err = nil
	}

	return err
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
		p.r.log("[%s] line: %q", p.name, sc.Text())
	}

	err := sc.Err()
	if err != nil {
		err = fmt.Errorf("in %q pipe: %w", p.name, err)
	}

	return err
}
