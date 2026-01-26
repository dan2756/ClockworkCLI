// Command cronlite runs commands on intervals from a plain text file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/dan2756/cronlite/internal/runner"
	"github.com/dan2756/cronlite/internal/schedule"
	"github.com/dan2756/cronlite/internal/taskfile"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const usage = `cronlite runs commands on intervals from a plain text file.

Usage:
  cronlite run   [-f FILE]          run the scheduler in the foreground
  cronlite check [-f FILE]          validate the task file
  cronlite next  EXPR [-n COUNT]    print the next fire times of a cron expression

Options:
  -f FILE      task file (default ./cronlite.txt, else ~/.config/cronlite/tasks)
  -h, --help   show help
  --version    print the version

Task file:
  every 5m                    ./scripts/sync.sh
  every 1h30m                 rsync -a ~/notes/ backup:/notes/
  cron "*/15 9-17 * * 1-5"    ./check-queue.sh
`

func main() {
	os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr))
}

func cli(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "-version", "--version", "version":
		fmt.Fprintln(stdout, "cronlite", versionString())
		return exitOK
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "next":
		return cmdNext(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "cronlite: unknown command %q\n\n%s", args[0], usage)
	return exitUsage
}

func versionString() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// newFlags returns a flag set for a subcommand with the shared -f flag.
func newFlags(name string, stderr io.Writer, file *string) *flag.FlagSet {
	fs := flag.NewFlagSet("cronlite "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(file, "f", "", "task `file` (default ./cronlite.txt, else ~/.config/cronlite/tasks)")
	return fs
}

// parseArgs parses fs, allowing flags before and after positional
// arguments, and returns the positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// parseFlags parses fs and rejects positional arguments.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagErrorCode(err), false
	}
	if len(pos) > 0 {
		fmt.Fprintf(fs.Output(), "%s: unexpected argument %q\n", fs.Name(), pos[0])
		return exitUsage, false
	}
	return exitOK, true
}

func flagErrorCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

// findTaskFile returns the task file to use: the -f flag if given, else
// ./cronlite.txt, else $XDG_CONFIG_HOME/cronlite/tasks (~/.config/cronlite/tasks).
func findTaskFile(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if _, err := os.Stat("cronlite.txt"); err == nil {
		return "cronlite.txt", nil
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no ./cronlite.txt and cannot find home directory: %v", err)
		}
		cfg = filepath.Join(home, ".config")
	}
	p := filepath.Join(cfg, "cronlite", "tasks")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("no task file: create ./cronlite.txt or %s, or pass -f FILE", p)
}

// loadTasks parses the task file, printing every error with its line number.
func loadTasks(flagValue string, stderr io.Writer) (string, []*taskfile.Task, bool) {
	path, err := findTaskFile(flagValue)
	if err != nil {
		fmt.Fprintln(stderr, "cronlite:", err)
		return "", nil, false
	}
	tasks, err := taskfile.ParseFile(path)
	if err != nil {
		printParseError(stderr, path, err)
		return path, nil, false
	}
	return path, tasks, true
}

func printParseError(w io.Writer, path string, err error) {
	var list taskfile.ErrorList
	if errors.As(err, &list) {
		for _, e := range list {
			fmt.Fprintf(w, "%s:%d: %s\n", path, e.Line, e.Msg)
		}
		return
	}
	fmt.Fprintln(w, "cronlite:", err)
}

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
	fs := newFlags("run", stderr, &file)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := &runner.Output{Stdout: stdout, Stderr: stderr}
	out.Eventf("cronlite", "loaded %d %s from %s", len(tasks), plural(len(tasks), "task", "tasks"), path)
	runner.Run(ctx, tasks, out)
	out.Eventf("cronlite", "stopped")
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
	now := time.Now()
	t := now
	for range *count {
		if t = c.Next(t); t.IsZero() {
			break
		}
		fmt.Fprintf(stdout, "%s  in %s\n", t.Format("Mon 2006-01-02 15:04 MST"), human(t.Sub(now)))
	}
	return exitOK
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
