// Package proc runs helper programs (sqlite3, a hook's command) safely: in
// their own process group, killed whole when their time is up, with output
// capped.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Spec is one run of a program.
type Spec struct {
	Args    []string // program and arguments; no shell
	Dir     string
	Env     []string // nil keeps the current environment
	Stdin   io.Reader
	Timeout time.Duration
	MaxOut  int64 // stdout past this many bytes is an error
}

// Result is what a run printed.
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}

var (
	// ErrTimeout means the program didn't finish in time.
	ErrTimeout = errors.New("did not finish within its timeout")
	// ErrTooMuch means the program printed more than MaxOut bytes.
	ErrTooMuch = errors.New("printed more than allowed")
)

// Run runs s. A program that exits with a status other than 0 is not an
// error here: the caller reads ExitCode and Stderr. Errors are for a
// program that couldn't start, ran out of time or printed too much.
func Run(ctx context.Context, s Spec) (Result, error) {
	if len(s.Args) == 0 {
		return Result{}, errors.New("no program to run")
	}
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, s.Args[0], s.Args[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	cmd.Stdin = s.Stdin
	// A child that keeps stdout open must not hold the run past its
	// timeout: the whole process group is killed, and the wait gives up.
	OwnGroup(cmd)
	cmd.WaitDelay = time.Second
	var out, errOut bytes.Buffer
	max := s.MaxOut
	if max <= 0 {
		max = 64 << 20
	}
	lw := &LimitWriter{W: &out, N: max}
	cmd.Stdout = lw
	cmd.Stderr = &LimitWriter{W: &errOut, N: 1 << 20}
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errOut.Bytes()}
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		return res, ErrTimeout
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
		err = nil
	}
	if err != nil {
		return res, fmt.Errorf("could not be started: %w", err)
	}
	if lw.Over() {
		return res, ErrTooMuch
	}
	return res, nil
}

// LimitWriter keeps the first N bytes written to it, plus one so a caller
// can tell the limit was passed, and drops the rest.
type LimitWriter struct {
	W    io.Writer
	N    int64
	over bool
}

func (l *LimitWriter) Write(p []byte) (int, error) {
	if l.over {
		return len(p), nil
	}
	if int64(len(p)) > l.N {
		l.W.Write(p[:l.N+1])
		l.over = true
		return len(p), nil
	}
	l.N -= int64(len(p))
	return l.W.Write(p)
}

// Over reports whether more than N bytes were written.
func (l *LimitWriter) Over() bool { return l.over }
