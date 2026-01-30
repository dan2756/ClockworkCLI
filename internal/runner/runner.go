package runner

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/dan2756/cronlite/internal/taskfile"
)

// Run schedules every task until ctx is cancelled. Each task runs in its
// own goroutine; when a run finishes the task waits for its next fire time.
func Run(ctx context.Context, tasks []*taskfile.Task, out *Output) {
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx, t, out)
		}()
	}
	wg.Wait()
}

func loop(ctx context.Context, t *taskfile.Task, out *Output) {
	for run := 1; ; run++ {
		next := t.Schedule.Next(time.Now())
		if next.IsZero() {
			out.Eventf(t.Name, "never fires again")
			return
		}
		wait := time.Until(next)
		if t.Jitter > 0 {
			wait += rand.N(t.Jitter)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		RunOnce(ctx, t, run, out)
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
