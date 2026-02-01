package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTasks(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cronlite.txt")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = cli(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func fixClock(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

func TestCheck(t *testing.T) {
	good := writeTasks(t, "every 5m true\nat 09:00 weekdays true\n")
	code, stdout, _ := runCLI("check", "-f", good)
	if code != exitOK || !strings.Contains(stdout, "2 tasks OK") {
		t.Errorf("check good file: code %d, stdout %q", code, stdout)
	}

	bad := writeTasks(t, "# header\nevery 5m true\nevery soon true\n\ncron \"* * *\" true\n")
	code, _, stderr := runCLI("check", "-f", bad)
	if code != exitFail {
		t.Errorf("check bad file: code %d, want %d", code, exitFail)
	}
	want := bad + `:3: every: invalid duration "soon" (use e.g. 30s, 5m, 1h30m, 2d)` + "\n" +
		bad + ":5: cron: want 5 fields (minute hour day-of-month month day-of-week), got 3\n"
	if stderr != want {
		t.Errorf("stderr:\n%s\nwant:\n%s", stderr, want)
	}
}

func TestCheckMissingFile(t *testing.T) {
	code, _, stderr := runCLI("check", "-f", filepath.Join(t.TempDir(), "nope.txt"))
	if code != exitFail || !strings.Contains(stderr, "no such file") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

func TestFindTaskFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := filepath.Join(dir, "config")
	t.Setenv("XDG_CONFIG_HOME", cfg)

	if _, err := findTaskFile(""); err == nil || !strings.Contains(err.Error(), "no task file") {
		t.Errorf("no files: err = %v", err)
	}
	global := filepath.Join(cfg, "cronlite", "tasks")
	os.MkdirAll(filepath.Dir(global), 0o755)
	os.WriteFile(global, nil, 0o644)
	if got, _ := findTaskFile(""); got != global {
		t.Errorf("got %q, want %q", got, global)
	}
	os.WriteFile("cronlite.txt", nil, 0o644)
	if got, _ := findTaskFile(""); got != "cronlite.txt" {
		t.Errorf("got %q, want ./cronlite.txt", got)
	}
	if got, _ := findTaskFile("other"); got != "other" {
		t.Errorf("-f ignored: got %q", got)
	}
}

func TestList(t *testing.T) {
	fixClock(t, time.Date(2026, 2, 1, 22, 45, 44, 0, time.UTC))
	path := writeTasks(t, `every 5m ./scripts/sync.sh
at 09:00,13:30 weekdays  name=stretch say "stretch"
cron "*/15 9-17 * * 1-5"  ./check-queue.sh
`)
	code, stdout, stderr := runCLI("list", "-f", path)
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	want := `NAME         SCHEDULE                  NEXT RUN                            COMMAND
sync         every 5m                  in 5m*     Sun 2026-02-01 22:50:44  ./scripts/sync.sh
stretch      at 09:00,13:30 weekdays   in 10h14m  Mon 2026-02-02 09:00     say "stretch"
check-queue  cron "*/15 9-17 * * 1-5"  in 10h14m  Mon 2026-02-02 09:00     ./check-queue.sh

* every schedules count from when cronlite run starts
`
	if stdout != want {
		t.Errorf("got:\n%s\nwant:\n%s", stdout, want)
	}
}

func TestOnce(t *testing.T) {
	path := writeTasks(t, `every 1h echo "first $CRONLITE_RUN"
every 1h name=broken exit 3
every 1h name=flaky onfail=continue exit 4
every 1h echo last
`)
	code, stdout, stderr := runCLI("once", "-f", path)
	if code != exitFail {
		t.Errorf("code %d, want %d", code, exitFail)
	}
	if !strings.Contains(stdout, "[echo] first 1") || !strings.Contains(stdout, "[echo-2] last") {
		t.Errorf("not every task ran:\n%s", stdout)
	}
	if !strings.Contains(stderr, "1 of 4 tasks failed: broken") {
		t.Errorf("stderr:\n%s", stderr)
	}

	ok := writeTasks(t, "every 1h true\nat 03:00 onfail=continue false\n")
	if code, _, stderr := runCLI("once", "-f", ok); code != exitOK || !strings.Contains(stderr, "2 tasks ok") {
		t.Errorf("code %d, stderr:\n%s", code, stderr)
	}
}

func TestNext(t *testing.T) {
	fixClock(t, time.Date(2026, 1, 23, 16, 0, 0, 0, time.UTC)) // a Friday
	code, stdout, _ := runCLI("next", "*/15 9-17 * * 1-5", "-n", "3")
	if code != exitOK {
		t.Fatalf("code %d", code)
	}
	want := `Fri 2026-01-23 16:15 UTC  in 15m
Fri 2026-01-23 16:30 UTC  in 30m
Fri 2026-01-23 16:45 UTC  in 45m
`
	if stdout != want {
		t.Errorf("got:\n%s\nwant:\n%s", stdout, want)
	}

	if code, _, stderr := runCLI("next", "61 * * * *"); code != exitFail || !strings.Contains(stderr, "out of range") {
		t.Errorf("bad expression: code %d, stderr %q", code, stderr)
	}
	if code, _, _ := runCLI("next"); code != exitUsage {
		t.Errorf("missing expression: code %d", code)
	}
	if code, _, _ := runCLI("next", "* * * * *", "-n", "0"); code != exitUsage {
		t.Errorf("-n 0: code %d", code)
	}
}

func TestUsageErrors(t *testing.T) {
	if code, _, _ := runCLI(); code != exitUsage {
		t.Errorf("no args: code %d", code)
	}
	if code, _, stderr := runCLI("frobnicate"); code != exitUsage || !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Errorf("unknown command: code %d, stderr %q", code, stderr)
	}
	if code, _, _ := runCLI("check", "extra"); code != exitUsage {
		t.Errorf("extra argument: code %d", code)
	}
	if code, _, _ := runCLI("run", "--grace", "soon"); code != exitUsage {
		t.Errorf("bad --grace: code %d", code)
	}
	if code, stdout, _ := runCLI("--help"); code != exitOK || !strings.Contains(stdout, "cronlite run") {
		t.Errorf("--help: code %d", code)
	}
	if code, stdout, _ := runCLI("--version"); code != exitOK || !strings.HasPrefix(stdout, "cronlite ") {
		t.Errorf("--version: code %d, %q", code, stdout)
	}
}
