package runner

import (
	"context"
	"os/exec"
	"time"

	"github.com/dan2756/cronlite/internal/taskfile"
)

// Exec runs the task's command with sh -c and waits for it to exit. Its
// output is line-prefixed onto out. Cancelling ctx kills the command.
func Exec(ctx context.Context, t *taskfile.Task, out *Output) error {
	stdout := out.lineWriter(out.Stdout, t.Name)
	stderr := out.lineWriter(out.Stderr, t.Name)
	defer stdout.Flush()
	defer stderr.Flush()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", t.Command)
	cmd.Dir = t.Dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
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
