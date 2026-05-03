package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dan2756/cronlite/internal/schedule"
	"github.com/dan2756/cronlite/internal/taskfile"
)

// safeBuffer is a bytes.Buffer that tests can read while tasks write.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var t0 = time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)

// fakeRuns is an Executor that records runs and blocks each one until it
// is released or killed.
type fakeRuns struct {
	started chan int
	release chan struct{}
	killed  chan int
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{
		started: make(chan int, 100),
		release: make(chan struct{}),
		killed:  make(chan int, 100),
	}
}

func (f *fakeRuns) exec(ctx context.Context, t *taskfile.Task, run int) error {
	f.started <- run
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		f.killed <- run
		return ErrKilled
	}
}

func testScheduler(tasks ...*taskfile.Task) (*Scheduler, *fakeRuns, *safeBuffer) {
	var log safeBuffer
	s := NewScheduler(&Output{Stdout: &log, Stderr: &log})
	f := newFakeRuns()
	s.Exec = f.exec
	s.Load(tasks, t0)
	return s, f, &log
}

func expectRun(t *testing.T, f *fakeRuns, want int) {
	t.Helper()
	select {
	case run := <-f.started:
		if run != want {
			t.Fatalf("started run %d, want %d", run, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("run %d never started", want)
	}
}

func expectNoRun(t *testing.T, f *fakeRuns) {
	t.Helper()
	select {
	case run := <-f.started:
		t.Fatalf("unexpected run %d", run)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestTickNeverOverlapsATask(t *testing.T) {
	s, f, log := testScheduler(task("sync", "true"))
	defer s.Kill()

	if next := s.tick(t0.Add(30 * time.Second)); !next.Equal(t0.Add(time.Minute)) {
		t.Fatalf("next = %v, want %v", next, t0.Add(time.Minute))
	}
	expectNoRun(t, f)

	s.tick(t0.Add(1 * time.Minute))
	expectRun(t, f, 1)
	s.tick(t0.Add(2 * time.Minute)) // still running: skipped
	s.tick(t0.Add(3 * time.Minute)) // and again
	expectNoRun(t, f)

	close(f.release)
	s.wg.Wait()
	if next := s.tick(t0.Add(4 * time.Minute)); !next.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("next = %v, want %v", next, t0.Add(5*time.Minute))
	}
	expectRun(t, f, 2)
	s.wg.Wait()

	if got := strings.Count(log.String(), "[sync] -- skipped: still running"); got != 2 {
		t.Errorf("got %d skip messages, want 2:\n%s", got, log)
	}
}

func TestTickTracksTasksIndependently(t *testing.T) {
	fast := task("fast", "true")
	slow := task("slow", "true")
	slow.Schedule = schedule.Every{Interval: 5 * time.Minute}
	s, f, _ := testScheduler(fast, slow)
	defer s.Kill()

	if next := s.tick(t0); !next.Equal(t0.Add(time.Minute)) {
		t.Errorf("soonest = %v, want fast's %v", next, t0.Add(time.Minute))
	}
	s.tick(t0.Add(time.Minute))
	expectRun(t, f, 1) // fast
	s.tick(t0.Add(5 * time.Minute))
	expectRun(t, f, 1) // slow, while fast is still busy
	expectNoRun(t, f)
	close(f.release)
}

func TestTickDropsMissedRuns(t *testing.T) {
	s, f, _ := testScheduler(task("sync", "true"))
	close(f.release)
	// The process was asleep for ten minutes: one run, not ten.
	next := s.tick(t0.Add(10*time.Minute + 15*time.Second))
	expectRun(t, f, 1)
	expectNoRun(t, f)
	if want := t0.Add(11*time.Minute + 15*time.Second); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
	s.wg.Wait()
}

func TestJitterPrefersTaskSetting(t *testing.T) {
	s, _, _ := testScheduler()
	s.rand = func(d time.Duration) time.Duration { return d }
	tk := task("x", "true")
	if got := s.jitter(tk); got != 0 {
		t.Errorf("no jitter: got %v", got)
	}
	s.Jitter = 10 * time.Second
	if got := s.jitter(tk); got != 10*time.Second {
		t.Errorf("global jitter: got %v", got)
	}
	tk.Jitter = time.Minute
	if got := s.jitter(tk); got != time.Minute {
		t.Errorf("task jitter: got %v", got)
	}
}

func TestShutdownWaitsForRunningTasks(t *testing.T) {
	s, f, log := testScheduler(task("sync", "true"))
	s.Grace = 5 * time.Second
	s.tick(t0.Add(time.Minute))
	expectRun(t, f, 1)
	time.AfterFunc(50*time.Millisecond, func() { close(f.release) })

	start := time.Now()
	s.Shutdown()
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Shutdown took %v", took)
	}
	select {
	case run := <-f.killed:
		t.Errorf("run %d was killed despite finishing within the grace period", run)
	default:
	}
	if !strings.Contains(log.String(), "waiting up to 5s for 1 running task") {
		t.Errorf("log:\n%s", log)
	}
	// Nothing fires after shutdown.
	s.tick(t0.Add(2 * time.Minute))
	expectNoRun(t, f)
}

func TestShutdownKillsAfterGrace(t *testing.T) {
	s, f, log := testScheduler(task("stuck", "true"))
	s.Grace = 50 * time.Millisecond
	s.tick(t0.Add(time.Minute))
	expectRun(t, f, 1)
	s.Shutdown()
	select {
	case <-f.killed:
	default:
		t.Error("stuck run was not killed")
	}
	if !strings.Contains(log.String(), "killing 1 running task") {
		t.Errorf("log:\n%s", log)
	}
}

func TestKillCutsGraceShort(t *testing.T) {
	s, f, _ := testScheduler(task("stuck", "true"))
	s.Grace = time.Hour
	s.tick(t0.Add(time.Minute))
	expectRun(t, f, 1)
	time.AfterFunc(50*time.Millisecond, s.Kill)
	start := time.Now()
	s.Shutdown()
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Shutdown took %v after Kill", took)
	}
}

func TestShutdownDropsRunsWaitingOnJitter(t *testing.T) {
	s, f, _ := testScheduler(task("sync", "true"))
	s.Jitter = time.Hour
	s.rand = func(d time.Duration) time.Duration { return d }
	s.tick(t0.Add(time.Minute))
	s.Shutdown()
	expectNoRun(t, f)
}

func TestRunWithRealCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real processes")
	}
	var stdout, stderr safeBuffer
	s := NewScheduler(&Output{Stdout: &stdout, Stderr: &stderr})
	s.Grace = 5 * time.Second
	tk := task("slow", "sleep 0.25; echo done")
	tk.Schedule = schedule.Every{Interval: 100 * time.Millisecond}
	s.Load([]*taskfile.Task{tk}, time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	s.Run(ctx, nil)

	if !strings.Contains(stderr.String(), "[slow] -- skipped: still running") {
		t.Errorf("no skipped runs logged:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "[slow] done") {
		t.Errorf("no run finished:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "killing") {
		t.Errorf("run was killed instead of finishing during the grace period:\n%s", stderr.String())
	}
}

func TestLoadKeepsScheduleOfUnchangedTasks(t *testing.T) {
	a := task("a", "true")
	b := task("b", "true")
	b.Schedule = schedule.Every{Interval: 5 * time.Minute}
	s, f, _ := testScheduler(a, b)
	defer s.Kill()
	s.tick(t0.Add(time.Minute)) // a runs and stays busy
	expectRun(t, f, 1)

	a2 := task("a", "true")
	a2.Timeout = time.Second // changed, but same schedule
	b2 := task("b", "true")
	b2.Schedule = schedule.Every{Interval: 10 * time.Minute}
	c := task("c", "true")
	reloadAt := t0.Add(90 * time.Second)
	ch := s.Load([]*taskfile.Task{a2, b2, c}, reloadAt)
	if want := (Changes{Added: 1, Changed: 2}); ch != want {
		t.Errorf("changes = %+v, want %+v", ch, want)
	}
	next := map[string]time.Time{}
	for _, e := range s.entries {
		next[e.task.Name] = e.next
	}
	if want := t0.Add(2 * time.Minute); !next["a"].Equal(want) {
		t.Errorf("a: next = %v, want %v (kept)", next["a"], want)
	}
	if want := reloadAt.Add(10 * time.Minute); !next["b"].Equal(want) {
		t.Errorf("b: next = %v, want %v (rescheduled)", next["b"], want)
	}
	if want := reloadAt.Add(time.Minute); !next["c"].Equal(want) {
		t.Errorf("c: next = %v, want %v (new)", next["c"], want)
	}

	// a's first run is still going, so the reloaded a must not overlap it.
	s.tick(t0.Add(2 * time.Minute))
	expectNoRun(t, f)

	if ch := s.Load([]*taskfile.Task{a2}, reloadAt); ch != (Changes{Removed: 2}) {
		t.Errorf("changes = %+v, want 2 removed", ch)
	}
	close(f.release)
}

func TestChangesString(t *testing.T) {
	if got := (Changes{}).String(); got != "no changes" {
		t.Errorf("got %q", got)
	}
	if got := (Changes{Added: 1, Removed: 2}).String(); got != "1 added, 2 removed" {
		t.Errorf("got %q", got)
	}
}

func writeFile(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestFileSourceWaitsForWritesToSettle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks")
	writeFile(t, path, "every 1m true\n", t0)
	src := NewFileSource(path)
	if src.Changed() {
		t.Fatal("unchanged file reported as changed")
	}
	writeFile(t, path, "every 2m true\n", t0.Add(time.Second))
	if src.Changed() {
		t.Error("change reported before it settled")
	}
	if !src.Changed() {
		t.Error("settled change not reported")
	}
	if src.Changed() {
		t.Error("change reported twice")
	}
	os.Remove(path)
	src.Changed()
	if !src.Changed() {
		t.Error("removal not reported")
	}
	if _, err := src.Load(); err == nil {
		t.Error("Load of a missing file succeeded")
	}
}

func TestRunReloadsChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cronlite.txt")
	writeFile(t, path, "every 1h name=hourly true\n", t0)
	var log safeBuffer
	s := NewScheduler(&Output{Stdout: &log, Stderr: &log})
	s.PollInterval = 10 * time.Millisecond
	s.Exec = newFakeRuns().exec
	src := NewFileSource(path)
	tasks, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.Load(tasks, time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx, src)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitFor := func(substr string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !strings.Contains(log.String(), substr) {
			if time.Now().After(deadline) {
				t.Fatalf("log never contained %q:\n%s", substr, log.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	writeFile(t, path, "every 1h name=hourly true\nevery soon broken\n", t0.Add(time.Second))
	waitFor(path + ":2: every: invalid duration")
	waitFor("not reloaded; keeping the previous schedule")

	writeFile(t, path, "every 1h name=hourly true\nat 09:00 name=morning true\n", t0.Add(2*time.Second))
	waitFor("reloaded " + path + ": 2 tasks (1 added)")

	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	if n != 2 {
		t.Errorf("%d tasks scheduled after reload, want 2", n)
	}
}
