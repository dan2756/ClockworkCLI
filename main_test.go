package main

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	_ "time/tzdata" // so DST tests run where the system zoneinfo is missing
)

// TestMain lets a test run cronlite as a child process: with
// CRONLITE_TEST_MAIN set, the test binary is cronlite.
func TestMain(m *testing.M) {
	if os.Getenv("CRONLITE_TEST_MAIN") != "" {
		main()
	}
	os.Exit(m.Run())
}

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

func TestListAndNextShowShortEvery(t *testing.T) {
	fixClock(t, time.Date(2026, 1, 23, 16, 0, 10, 0, time.UTC)) // a Friday
	path := writeTasks(t, "every 250ms name=tick true\n")
	code, stdout, stderr := runCLI("list", "-f", path)
	if code != exitOK {
		t.Fatalf("list: code %d, stderr %q", code, stderr)
	}
	want := `NAME  SCHEDULE     NEXT RUN                                COMMAND
tick  every 250ms  in 250ms*  Fri 2026-01-23 16:00:10.250  true

* every schedules count from when cronlite run starts
`
	if stdout != want {
		t.Errorf("list got:\n%s\nwant:\n%s", stdout, want)
	}

	code, stdout, stderr = runCLI("next", "every 45s", "-n", "2")
	if code != exitOK {
		t.Fatalf("next 45s: code %d, stderr %q", code, stderr)
	}
	want = `Fri 2026-01-23 16:00:55 UTC  in 45s
Fri 2026-01-23 16:01:40 UTC  in 1m30s
`
	if stdout != want {
		t.Errorf("next 45s got:\n%s\nwant:\n%s", stdout, want)
	}

	code, stdout, stderr = runCLI("next", "every 250ms", "-n", "2")
	if code != exitOK {
		t.Fatalf("next 250ms: code %d, stderr %q", code, stderr)
	}
	want = `Fri 2026-01-23 16:00:10.250 UTC  in 250ms
Fri 2026-01-23 16:00:10.500 UTC  in 500ms
`
	if stdout != want {
		t.Errorf("next 250ms got:\n%s\nwant:\n%s", stdout, want)
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

	// Any task file schedule works, and --from moves the starting point.
	code, stdout, _ = runCLI("next", "at 09:00 weekdays", "--from", "2026-03-06 10:00", "-n", "2")
	want = `Mon 2026-03-09 09:00 UTC  in 2d23h
Tue 2026-03-10 09:00 UTC  in 3d23h
`
	if code != exitOK || stdout != want {
		t.Errorf("at schedule: code %d, got:\n%s\nwant:\n%s", code, stdout, want)
	}
	code, stdout, _ = runCLI("next", "--from=2026-01-01", "every 1d12h", "-n", "2")
	want = `Fri 2026-01-02 12:00 UTC  in 1d12h
Sun 2026-01-04 00:00 UTC  in 3d
`
	if code != exitOK || stdout != want {
		t.Errorf("every schedule: code %d, got:\n%s\nwant:\n%s", code, stdout, want)
	}
	if code, _, _ := runCLI("next", "@daily", "--from", "tomorrow"); code != exitUsage {
		t.Errorf("bad --from: code %d", code)
	}
	if code, _, stderr := runCLI("next", "at 25:00"); code != exitFail || !strings.Contains(stderr, "invalid time") {
		t.Errorf("bad at schedule: code %d, stderr %q", code, stderr)
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

func TestNextFromDSTGap(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 02:30 does not exist on the spring-forward day. Parsing it used to
	// succeed as 01:30, so next started an hour early.
	fixClock(t, time.Date(2026, 3, 8, 12, 0, 0, 0, ny))
	code, stdout, stderr := runCLI("next", "30 2 * * *", "--from", "2026-03-08 02:30", "-n", "1")
	if code != exitUsage || !strings.Contains(stderr, "2026-03-08 02:30 does not exist in America/New_York") {
		t.Errorf("gap: code %d, stderr %q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("gap wrote %q", stdout)
	}

	// The repeated hour does exist; use the first occurrence. A time after
	// the jump is a real time too.
	got, err := parseTime("2026-11-01 01:30", ny)
	if err != nil {
		t.Fatal(err)
	}
	if name, off := got.Zone(); name != "EDT" || off != -4*3600 || got.Hour() != 1 || got.Minute() != 30 {
		t.Errorf("repeated hour = %s, want the first 01:30 EDT", got.Format(time.RFC3339))
	}
	code, stdout, stderr = runCLI("next", "0 3 * * *", "--from", "2026-03-08 03:00", "-n", "1")
	if code != exitOK || !strings.Contains(stdout, "Mon 2026-03-09 03:00 EDT") {
		t.Errorf("after jump: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestRunSurvivesClosedOutputAndStopsOnHangup(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cronlite and real tasks")
	}
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("SIGHUP is ignored (nohup)")
	}
	path := writeTasks(t, "every 200ms name=chatty echo hello\nevery 200ms name=quiet sleep 30\n")
	logPath := filepath.Join(t.TempDir(), "stderr")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	// Nobody reads stdout, as with "cronlite run | tee log" once tee is gone.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()

	cmd := exec.Command(os.Args[0], "run", "-f", path, "--grace", "200ms")
	cmd.Env = append(os.Environ(), "CRONLITE_TEST_MAIN=1")
	cmd.Stdout, cmd.Stderr = w, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer cmd.Process.Kill()
	log := func() string {
		b, _ := os.ReadFile(logPath)
		return string(b)
	}

	// chatty's output has hit the closed pipe by the time its second run
	// is over; quiet is still running when the terminal hangs up.
	deadline := time.After(5 * time.Second)
	for !strings.Contains(log(), "[chatty] -- run 2 ok") || !strings.Contains(log(), "[quiet] -- run 1 started") {
		select {
		case err := <-exited:
			t.Fatalf("cronlite exited early: %v\n%s", err, log())
		case <-deadline:
			t.Fatalf("tasks never ran:\n%s", log())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("cronlite: %v\n%s", err, log())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("cronlite did not stop after SIGHUP:\n%s", log())
	}
	for _, want := range []string{"hangup: shutting down", "killing 1 running task", "[cronlite] -- stopped"} {
		if !strings.Contains(log(), want) {
			t.Errorf("log missing %q:\n%s", want, log())
		}
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
