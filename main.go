package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func main() {
	var r runner

	cmd := &cobra.Command{
		Use: "logtime [FLAGS] -- COMMAND [ARG1 [ARG2 ...]]",
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.run(args)
		},
	}

	cmd.Flags().StringVarP(&r.File, "filename", "o", "", "Redirect output here")
	cmd.Flags().BoolVarP(&r.Append, "append", "a", false, "Append to file")
	cmd.Flags().StringVarP(&r.Format, "format", "f", "%Y%m%d-%H%M%S.%L", "Redirect output here")

	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

type runner struct {
	File   string
	Format string
	Append bool
}

func (r *runner) run(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)

	return nil
}

type pipe struct {
	r         *runner
	name      string
	targetPtr *io.Writer
	pr        *os.File
	pw        *os.File
}

func (r *runner) openPipe(target *io.Writer, name string) (*pipe, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	return &pipe{
		r:         r,
		name:      name,
		targetPtr: target,
		pr:        pr,
		pw:        pw,
	}, nil
}

func (p *pipe) start(c chan error) error {
	if err := p.pw.Close(); err != nil {
		return fmt.Errorf("closing parent write pipe: %w", err)
	}

	go p.runUntilEOF(c)

	return nil
}

func (p *pipe) runUntilEOF(c chan error) {
	sc := bufio.NewScanner(p.pr)
	for sc.Scan() {
	}

	c <- sc.Err()
}
