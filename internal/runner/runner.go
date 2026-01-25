package runner

import (
	"context"
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
	for {
		next := t.Schedule.Next(time.Now())
		if next.IsZero() {
			out.Eventf(t.Name, "never fires again")
			return
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		runOnce(ctx, t, out)
	}
}

func runOnce(ctx context.Context, t *taskfile.Task, out *Output) {
	out.Eventf(t.Name, "started")
	start := time.Now()
	err := Exec(ctx, t, out)
	took := elapsed(time.Since(start))
	if err != nil {
		out.Eventf(t.Name, "failed after %s: %v", took, err)
		return
	}
	out.Eventf(t.Name, "finished in %s", took)
}
