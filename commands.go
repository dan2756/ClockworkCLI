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
	"github.com/dan2756/cronlite/internal/taskfile"
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
	path, err := findTaskFile(file)
	if err != nil {
		fmt.Fprintln(stderr, "cronlite:", err)
		return exitFail
	}
	// Stat the file before parsing it so that an edit made in between is
	// picked up by the first poll.
	src := runner.NewFileSource(path)
	tasks, err := src.Load()
	if err != nil {
		printParseError(stderr, path, err)
		return exitFail
	}

	defer survivePipeErrors()()
	out := &runner.Output{Stdout: stdout, Stderr: stderr}
	s := runner.NewScheduler(out)
	s.Jitter = time.Duration(jitter)
	s.Grace = time.Duration(grace)
	s.Load(tasks, time.Now())
	out.Eventf("cronlite", "loaded %d %s from %s; watching it for changes", len(tasks), plural(len(tasks), "task", "tasks"), path)

	// The first signal stops scheduling and lets running tasks finish; a
	// second one kills them.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, stopSignals()...)
	defer signal.Stop(sigs)
	go func() {
		sig := <-sigs
		out.Eventf("cronlite", "%v: shutting down (send again to kill running tasks)", sig)
		cancel()
		<-sigs
		s.Kill()
	}()

	s.Run(ctx, src)
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
			rel, abs = "in "+human(next.Sub(t0)), formatTime(next, false)
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

// stopSignals returns the signals that make run and once stop their tasks:
// Ctrl-C, kill, and the terminal going away. Tasks run in process groups
// of their own, out of the terminal's reach, so cronlite must not just die
// from any of these. SIGHUP is left alone when it is ignored (nohup).
func stopSignals() []os.Signal {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return sigs
}

// survivePipeErrors makes writes to a closed stdout or stderr fail instead
// of killing cronlite with SIGPIPE, so that, for example, Ctrl-C on
// "cronlite run | tee log" still shuts the tasks down properly. Tasks
// still get the default SIGPIPE behaviour. Call the returned function to
// undo it.
func survivePipeErrors() (undo func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGPIPE)
	return func() { signal.Stop(c) }
}

// formatTime prints a fire time, with seconds and fractions only when they
// are not zero. zone appends the time zone abbreviation.
func formatTime(t time.Time, zone bool) string {
	var layout string
	switch {
	case t.Nanosecond() == 0 && t.Second() == 0:
		layout = "Mon 2006-01-02 15:04"
	case t.Nanosecond() == 0:
		layout = "Mon 2006-01-02 15:04:05"
	case t.Nanosecond()%int(time.Millisecond) == 0:
		layout = "Mon 2006-01-02 15:04:05.000"
	default:
		layout = "Mon 2006-01-02 15:04:05.000000000"
	}
	if zone {
		layout += " MST"
	}
	return t.Format(layout)
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

	defer survivePipeErrors()()
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals()...)
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
	from := fs.String("from", "", "start from `TIME` (YYYY-MM-DD [HH:MM], local time) instead of now")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: cronlite next EXPR|SCHEDULE [-n COUNT] [--from TIME]

Prints the next fire times of a cron expression or any task file schedule:
  cronlite next "*/15 9-17 * * 1-5" -n 5
  cronlite next "at 09:00,13:30 weekdays"
  cronlite next "30 2 * * *" --from 2026-03-07

`)
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
	sched, err := parseNextArg(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "cronlite next: %v\n", err)
		return exitFail
	}
	t0 := now()
	if *from != "" {
		if t0, err = parseTime(*from, t0.Location()); err != nil {
			fmt.Fprintf(stderr, "cronlite next: --from: %v\n", err)
			return exitUsage
		}
	}
	t := t0
	for range *count {
		if t = sched.Next(t); t.IsZero() {
			break
		}
		fmt.Fprintf(stdout, "%s  in %s\n", formatTime(t, true), human(t.Sub(t0)))
	}
	return exitOK
}

// parseNextArg accepts a bare cron expression or a full schedule as written
// in a task file.
func parseNextArg(s string) (schedule.Schedule, error) {
	switch kw, _, _ := strings.Cut(strings.TrimSpace(s), " "); kw {
	case "every", "at", "cron":
		return taskfile.ParseSchedule(s)
	}
	return schedule.ParseCron(s)
}

// parseTime parses a date with an optional time of day in loc.
func parseTime(s string, loc *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		// Spring-forward gaps have no such clock reading. ParseInLocation
		// still returns a time, but it is a different reading (an hour
		// earlier in America/New_York), so next would start from the wrong
		// instant. A repeated hour formats back to the same reading.
		if t.Format(layout) != s {
			return time.Time{}, fmt.Errorf("%s does not exist in %s (daylight saving time gap)", s, loc)
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(loc), nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q (want YYYY-MM-DD or YYYY-MM-DD HH:MM)", s)
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
// useful: "250ms", "45s", "3m20s", "13h8m", "2d3h".
func human(d time.Duration) string {
	switch {
	case d < time.Second:
		// Rounding to the second turns every 250ms into "in 0s".
		if d < time.Millisecond {
			return schedule.FormatDuration(d)
		}
		return schedule.FormatDuration(d.Round(time.Millisecond))
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
