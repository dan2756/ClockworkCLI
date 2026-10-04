package runner

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dan2756/cronlite/internal/schedule"
	"github.com/dan2756/cronlite/internal/taskfile"
)

func task(name, command string) *taskfile.Task {
	return &taskfile.Task{Name: name, Command: command, Schedule: schedule.Every{Interval: time.Minute}}
}

func TestExecOutputAndEnv(t *testing.T) {
	o, stdout, stderr := fixedOutput()
	tk := task("hello", `echo "task=$CRONLITE_TASK run=$CRONLITE_RUN"; echo oops >&2; pwd`)
	tk.Dir = t.TempDir()
	if err := Exec(context.Background(), tk, 7, o); err != nil {
		t.Fatal(err)
	}
	want := "2026-01-25 19:51:18 [hello] task=hello run=7\n" +
		"2026-01-25 19:51:18 [hello] " + tk.Dir + "\n"
	if stdout.String() != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	if want := "2026-01-25 19:51:18 [hello] oops\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestExecExitStatus(t *testing.T) {
	o, _, _ := fixedOutput()
	err := Exec(context.Background(), task("fail", "exit 3"), 1, o)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("err = %v, want exit status 3", err)
	}
}

func TestExecTimeoutKillsProcessGroup(t *testing.T) {
	o, stdout, _ := fixedOutput()
	// The background sleep inherits stdout; if only the shell were killed
	// it would keep the pipe open and Exec would not return until it exits.
	tk := task("slow", "sleep 5 & echo started; wait")
	tk.Timeout = 200 * time.Millisecond
	start := time.Now()
	err := Exec(context.Background(), tk, 1, o)
	took := time.Since(start)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want TimeoutError", err)
	}
	// The killed sleep stays in the group as a zombie until init reaps it,
	// which some inits only do every so often, so Exec may wait for it
	// until killDelay.
	if took > killDelay+time.Second {
		t.Errorf("Exec took %v; process group not killed", took)
	}
	if !strings.Contains(stdout.String(), "[slow] started") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestExecTimeoutEscalatesToSIGKILL(t *testing.T) {
	o, _, _ := fixedOutput()
	tk := task("stubborn", `trap "" TERM; sleep 5 & wait; wait`)
	tk.Timeout = 100 * time.Millisecond
	start := time.Now()
	err := Exec(context.Background(), tk, 1, o)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want TimeoutError", err)
	}
	if took := time.Since(start); took > killDelay+2*time.Second {
		t.Errorf("Exec took %v", took)
	}
}

func TestExecTimeoutKillsWhatOutlivesTheShell(t *testing.T) {
	var stdout, stderr safeBuffer
	o := &Output{Stdout: &stdout, Stderr: &stderr}
	// The shell dies from SIGTERM, but the inner one ignores it and keeps
	// the output pipe open; only SIGKILL to the group gets rid of it.
	tk := task("stubborn", `sh -c 'trap "" TERM; sleep 5'; echo after`)
	tk.Timeout = 100 * time.Millisecond
	start := time.Now()
	err := Exec(context.Background(), tk, 1, o)
	took := time.Since(start)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want TimeoutError", err)
	}
	if took < killDelay || took > killDelay+2*time.Second {
		t.Errorf("Exec took %v, want about %v (SIGTERM, then SIGKILL)", took, killDelay)
	}
	if strings.Contains(stderr.String(), "left a background process") {
		t.Errorf("the process ignoring SIGTERM was not killed:\n%s", stderr.String())
	}
}

func TestExecContextCancelKills(t *testing.T) {
	o, _, _ := fixedOutput()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := Exec(ctx, task("long", "sleep 5 & wait"), 1, o)
	if !errors.Is(err, ErrKilled) {
		t.Errorf("err = %v, want ErrKilled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Exec took %v after cancel", took)
	}
}

func TestRunOnceAllowFail(t *testing.T) {
	o, _, stderr := fixedOutput()
	tk := task("flaky", "exit 1")
	if err := RunOnce(context.Background(), tk, 1, o); err == nil {
		t.Error("RunOnce succeeded for a failing task")
	}
	tk.AllowFail = true
	if err := RunOnce(context.Background(), tk, 2, o); err != nil {
		t.Errorf("RunOnce with onfail=continue = %v", err)
	}
	log := stderr.String()
	for _, want := range []string{
		"[flaky] -- run 1 started",
		"[flaky] -- run 1 failed: exit status 1 (",
		"[flaky] -- run 2 failed: exit status 1 (",
		"ignored: onfail=continue)",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
}

func TestExecDoesNotWaitForBackgroundProcesses(t *testing.T) {
	var stdout, stderr safeBuffer
	o := &Output{Stdout: &stdout, Stderr: &stderr}
	tk := task("bg", "(sleep 2.5; echo late) & echo early")
	start := time.Now()
	if err := Exec(context.Background(), tk, 4, o); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Exec took %v; it waited for the background process", took)
	}
	if !strings.Contains(stdout.String(), "[bg] early") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[bg] -- run 4 left a background process holding its output") {
		t.Errorf("stderr = %q", stderr.String())
	}
	// The background process's output still arrives, prefixed as usual.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), "[bg] late") {
		if time.Now().After(deadline) {
			t.Fatalf("background output never arrived: %q", stdout.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExecStartFailure(t *testing.T) {
	o, _, _ := fixedOutput()
	tk := task("nodir", "true")
	tk.Dir = filepath.Join(t.TempDir(), "missing")
	if err := Exec(context.Background(), tk, 1, o); err == nil {
		t.Error("Exec in a missing directory succeeded")
	}
}
