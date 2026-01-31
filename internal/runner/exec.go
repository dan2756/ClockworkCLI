package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/dan2756/cronlite/internal/schedule"
	"github.com/dan2756/cronlite/internal/taskfile"
)

// killDelay is how long a timed-out task gets between SIGTERM and SIGKILL.
const killDelay = 2 * time.Second

// ErrKilled is returned by Exec when its context is cancelled.
var ErrKilled = errors.New("killed")

// A TimeoutError reports that a run exceeded its timeout and was killed.
type TimeoutError struct {
	After time.Duration
}

func (e *TimeoutError) Error() string {
	return "timed out after " + schedule.FormatDuration(e.After)
}

// Exec runs the task's command with sh -c in a process group of its own
// and waits for it to exit. Output is line-prefixed onto out, and the
// command sees CRONLITE_TASK and CRONLITE_RUN in its environment.
//
// When the task's timeout expires the whole process group gets SIGTERM,
// then SIGKILL if it is still around shortly after. Cancelling ctx kills
// the group at once.
func Exec(ctx context.Context, t *taskfile.Task, run int, out *Output) error {
	stdout := out.lineWriter(out.Stdout, t.Name)
	stderr := out.lineWriter(out.Stderr, t.Name)
	defer stdout.Flush()
	defer stderr.Flush()

	cmd := exec.Command("/bin/sh", "-c", t.Command)
	cmd.Dir = t.Dir
	cmd.Env = append(os.Environ(),
		"CRONLITE_TASK="+t.Name,
		"CRONLITE_RUN="+strconv.Itoa(run),
	)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timeout <-chan time.Time
	if t.Timeout > 0 {
		timer := time.NewTimer(t.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case err := <-done:
		return err
	case <-timeout:
		signalGroup(cmd, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(killDelay):
			signalGroup(cmd, syscall.SIGKILL)
			<-done
		case <-ctx.Done():
			signalGroup(cmd, syscall.SIGKILL)
			<-done
		}
		return &TimeoutError{After: t.Timeout}
	case <-ctx.Done():
		signalGroup(cmd, syscall.SIGKILL)
		<-done
		return ErrKilled
	}
}

// RunOnce executes one run of t, logging when it starts and how it ended.
// It returns the run's error unless the task allows failure.
func RunOnce(ctx context.Context, t *taskfile.Task, run int, out *Output) error {
	out.Eventf(t.Name, "run %d started", run)
	start := time.Now()
	err := Exec(ctx, t, run, out)
	took := elapsed(time.Since(start))
	note := ""
	if t.AllowFail {
		note = ", ignored: onfail=continue"
	}
	var timeout *TimeoutError
	switch {
	case err == nil:
		out.Eventf(t.Name, "run %d ok (%s)", run, took)
	case errors.As(err, &timeout):
		out.Eventf(t.Name, "run %d %v, process group killed%s", run, err, note)
	default:
		out.Eventf(t.Name, "run %d failed: %v (%s%s)", run, err, took, note)
	}
	if t.AllowFail {
		return nil
	}
	return err
}

// elapsed formats a run time with sensible precision.
func elapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(10 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
