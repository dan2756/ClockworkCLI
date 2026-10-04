package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
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
// A run ends when the shell exits. Anything it left running in the
// background keeps its output flowing to out, but is not waited for.
//
// When the task's timeout expires the whole process group gets SIGTERM,
// then SIGKILL if it is still around shortly after. Cancelling ctx kills
// the group at once.
func Exec(ctx context.Context, t *taskfile.Task, run int, out *Output) error {
	cmd := exec.Command("/bin/sh", "-c", t.Command)
	cmd.Dir = t.Dir
	cmd.Env = append(os.Environ(),
		"CRONLITE_TASK="+t.Name,
		"CRONLITE_RUN="+strconv.Itoa(run),
	)
	setProcessGroup(cmd)
	started, copied, err := pipeOutput(cmd, out, t.Name)
	if err != nil {
		return err
	}
	err = cmd.Start()
	started()
	if err != nil {
		return err
	}
	err = wait(ctx, cmd, t.Timeout)

	// Normally the pipes hit EOF as soon as the shell exits. If they do not,
	// a background process still holds them; let it keep writing.
	select {
	case <-copied:
	case <-time.After(outputWait):
		out.Eventf(t.Name, "run %d left a background process holding its output", run)
	}
	return err
}

// outputWait is how long Exec waits for a finished command's output to
// drain before leaving the rest to background readers.
const outputWait = time.Second

// pipeOutput connects the command's stdout and stderr to line writers via
// OS pipes. Handing exec.Cmd plain writers instead would make Wait block
// until every process holding the pipes exits, so a task that starts
// something in the background would look busy for as long as that runs.
//
// started must be called once the command has started (or failed to);
// copied is closed when both pipes have been drained.
func pipeOutput(cmd *exec.Cmd, out *Output, name string) (started func(), copied <-chan struct{}, err error) {
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	done := make(chan struct{})
	var wg sync.WaitGroup
	for _, p := range []struct {
		r *os.File
		w *lineWriter
	}{
		{outR, out.lineWriter(out.Stdout, name)},
		{errR, out.lineWriter(out.Stderr, name)},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(p.w, p.r)
			p.w.Flush()
			p.r.Close()
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	// Once the child has its copies, ours must go, or the readers would
	// never see EOF.
	started = func() {
		outW.Close()
		errW.Close()
	}
	return started, done, nil
}

// wait waits for the started command, enforcing its timeout and killing
// it if ctx is cancelled.
func wait(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case err := <-done:
		return err
	case <-expired:
		stopGroup(ctx, cmd, done)
		return &TimeoutError{After: timeout}
	case <-ctx.Done():
		signalGroup(cmd, syscall.SIGKILL)
		<-done
		return ErrKilled
	}
}

// groupPoll is how often stopGroup checks whether a timed-out process
// group is gone.
const groupPoll = 50 * time.Millisecond

// stopGroup sends SIGTERM to the command's process group, then SIGKILL
// after killDelay (or as soon as ctx is cancelled) if anything in the group
// is still there. The shell itself may die from SIGTERM while something it
// started ignores it, so it is the group that counts, not the shell. done
// delivers the shell's exit; stopGroup returns once it has.
func stopGroup(ctx context.Context, cmd *exec.Cmd, done <-chan error) {
	signalGroup(cmd, syscall.SIGTERM)
	kill := time.NewTimer(killDelay)
	defer kill.Stop()
	poll := time.NewTicker(groupPoll)
	defer poll.Stop()
	for exited := false; !exited || groupAlive(cmd); {
		select {
		case <-done:
			exited, done = true, nil
			continue
		case <-poll.C:
			continue
		case <-kill.C:
		case <-ctx.Done():
		}
		signalGroup(cmd, syscall.SIGKILL)
		if !exited {
			<-done
		}
		return
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
