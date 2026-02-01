// Command cronlite runs commands on intervals from a plain text file.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/dan2756/cronlite/internal/taskfile"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	exitOK          = 0
	exitFail        = 1 // a task failed, or the task file is invalid
	exitUsage       = 2
	exitInterrupted = 130
)

const usage = `cronlite runs commands on intervals from a plain text file.

Usage:
  cronlite run   [-f FILE] [--jitter DUR] [--grace DUR]
  cronlite list  [-f FILE]
  cronlite check [-f FILE]
  cronlite once  [-f FILE]
  cronlite next  EXPR [-n COUNT]

Commands:
  run     run the scheduler in the foreground until Ctrl-C
  list    show each task with its schedule and next run time
  check   validate the task file, printing errors with line numbers
  once    run every task once, one after another; exit 1 if any failed
  next    print the next fire times of a cron expression

Options:
  -f FILE        task file (default ./cronlite.txt, else ~/.config/cronlite/tasks)
  --jitter DUR   run: delay each run by a random amount up to DUR
  --grace DUR    run: on shutdown, wait this long for running tasks (default 10s)
  -h, --help     show help
  --version      print the version

Task file:
  every 5m                    ./scripts/sync.sh
  every 1h30m                 rsync -a ~/notes/ backup:/notes/
  at 09:00,13:30 weekdays     say "stretch"
  cron "*/15 9-17 * * 1-5"    ./check-queue.sh
  every 10s timeout=5s name=ping curl -fsS https://example.com/health

Task options (between the schedule and the command):
  timeout=DUR      kill the task's process group after DUR
  name=NAME        name shown in output (default: derived from the command)
  jitter=DUR       delay each run by a random amount up to DUR
  dir=PATH         working directory (default: the task file's directory)
  onfail=continue  report failures but do not treat them as errors
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
	case "list", "ls":
		return cmdList(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "once":
		return cmdOnce(args[1:], stdout, stderr)
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
