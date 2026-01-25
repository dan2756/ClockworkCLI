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

	"github.com/dan2756/cronlite/internal/runner"
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
  cronlite run   [-f FILE]    run the scheduler in the foreground
  cronlite check [-f FILE]    validate the task file

Options:
  -f FILE      task file (default ./cronlite.txt, else ~/.config/cronlite/tasks)
  -h, --help   show help
  --version    print the version

Task file:
  every 5m       ./scripts/sync.sh
  every 1h30m    rsync -a ~/notes/ backup:/notes/
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

// parseFlags parses fs and rejects positional arguments.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "%s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return exitUsage, false
	}
	return exitOK, true
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

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
