package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dan2756/cronlite/internal/runner"
	"github.com/dan2756/cronlite/internal/schedule"
)

// now is the clock used by list and next; tests replace it.
var now = time.Now

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	var file string
	fs := newFlags("check", stderr, &file)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	path, tasks, ok := loadTasks(file, stderr)
	if !ok {
		return exitFail
	}
	fmt.Fprintf(stdout, "%s: %d %s OK\n", path, len(tasks), plural(len(tasks), "task", "tasks"))
	return exitOK
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	var file string
	jitter := durationFlag(0)
	grace := durationFlag(10 * time.Second)
	fs := newFlags("run", stderr, &file)
	fs.Var(&jitter, "jitter", "delay each run by a random amount up to `DUR` (tasks may set their own)")
	fs.Var(&grace, "grace", "on shutdown, wait up to `DUR` for running tasks before killing them")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	path, tasks, ok := loadTasks(file, stderr)
	if !ok {
		return exitFail
	}
	if len(tasks) == 0 {
		fmt.Fprintf(stderr, "cronlite: no tasks in %s\n", path)
		return exitFail
	}

	out := &runner.Output{Stdout: stdout, Stderr: stderr}
	s := runner.NewScheduler(out)
	s.Jitter = time.Duration(jitter)
	s.Grace = time.Duration(grace)
	s.Load(tasks, time.Now())
	out.Eventf("cronlite", "loaded %d %s from %s", len(tasks), plural(len(tasks), "task", "tasks"), path)

	// The first signal stops scheduling and lets running tasks finish; a
	// second one kills them.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		sig := <-sigs
		out.Eventf("cronlite", "%v: shutting down (send again to kill running tasks)", sig)
		cancel()
		<-sigs
		s.Kill()
	}()

	s.Run(ctx)
	out.Eventf("cronlite", "stopped")
	return exitOK
}

func cmdList(args []string, stdout, stderr io.Writer) int {
	var file string
	fs := newFlags("list", stderr, &file)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	_, tasks, ok := loadTasks(file, stderr)
	if !ok {
		return exitFail
	}
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "no tasks")
		return exitOK
	}
	t0 := now()
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSCHEDULE\tNEXT RUN\t\tCOMMAND")
	footnote := false
	for _, t := range tasks {
		rel, abs := "never", ""
		if next := t.Schedule.Next(t0); !next.IsZero() {
			rel, abs = "in "+human(next.Sub(t0)), formatTime(next)
		}
		if _, ok := t.Schedule.(schedule.Every); ok {
			rel += "*"
			footnote = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Name, t.Schedule, rel, abs, t.Command)
	}
	tw.Flush()
	if footnote {
		fmt.Fprintln(stdout, "\n* every schedules count from when cronlite run starts")
	}
	return exitOK
}

// formatTime prints a fire time, with seconds only when they matter.
func formatTime(t time.Time) string {
	if t.Second() != 0 {
		return t.Format("Mon 2006-01-02 15:04:05")
	}
	return t.Format("Mon 2006-01-02 15:04")
}

func cmdOnce(args []string, stdout, stderr io.Writer) int {
	var file string
	fs := newFlags("once", stderr, &file)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	_, tasks, ok := loadTasks(file, stderr)
	if !ok {
		return exitFail
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := &runner.Output{Stdout: stdout, Stderr: stderr}
	var failed []string
	for _, t := range tasks {
		if err := runner.RunOnce(ctx, t, 1, out); err != nil {
			failed = append(failed, t.Name)
		}
		if ctx.Err() != nil {
			out.Eventf("cronlite", "interrupted")
			return exitInterrupted
		}
	}
	n := len(tasks)
	if len(failed) > 0 {
		out.Eventf("cronlite", "%d of %d %s failed: %s", len(failed), n, plural(n, "task", "tasks"), strings.Join(failed, ", "))
		return exitFail
	}
	out.Eventf("cronlite", "%d %s ok", n, plural(n, "task", "tasks"))
	return exitOK
}

func cmdNext(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cronlite next", flag.ContinueOnError)
	fs.SetOutput(stderr)
	count := fs.Int("n", 5, "number of fire times to print")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: cronlite next EXPR [-n COUNT]\n\n"+
			"Prints the next fire times of a cron expression, e.g.\n"+
			"  cronlite next \"*/15 9-17 * * 1-5\" -n 5\n\n")
		fs.PrintDefaults()
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagErrorCode(err)
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitUsage
	}
	if *count < 1 {
		fmt.Fprintln(stderr, "cronlite next: -n must be at least 1")
		return exitUsage
	}
	c, err := schedule.ParseCron(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "cronlite next: %v\n", err)
		return exitFail
	}
	t0 := now()
	t := t0
	for range *count {
		if t = c.Next(t); t.IsZero() {
			break
		}
		fmt.Fprintf(stdout, "%s  in %s\n", t.Format("Mon 2006-01-02 15:04 MST"), human(t.Sub(t0)))
	}
	return exitOK
}

// durationFlag is a flag.Value accepting ParseDuration syntax.
type durationFlag time.Duration

func (d *durationFlag) String() string { return schedule.FormatDuration(time.Duration(*d)) }

func (d *durationFlag) Set(s string) error {
	v, err := schedule.ParseDuration(s)
	if err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("must not be negative")
	}
	*d = durationFlag(v)
	return nil
}

// human formats a duration for people, with no more precision than is
// useful: "45s", "3m20s", "13h8m", "2d3h".
func human(d time.Duration) string {
	switch {
	case d < time.Hour:
		d = d.Round(time.Second)
	case d < 24*time.Hour:
		d = d.Round(time.Minute)
	default:
		d = d.Round(time.Hour)
	}
	return schedule.FormatDuration(d)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
