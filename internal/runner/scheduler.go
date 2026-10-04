package runner

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/dan2756/cronlite/internal/schedule"
	"github.com/dan2756/cronlite/internal/taskfile"
)

// maxWait caps how long the scheduler sleeps between checks, so that it
// notices quickly when the wall clock jumps (for example after the machine
// wakes from sleep).
const maxWait = 30 * time.Second

// An Executor performs one run of a task. It must return promptly once
// ctx is cancelled.
type Executor func(ctx context.Context, t *taskfile.Task, run int) error

// A Scheduler fires tasks on their schedules. A task never overlaps
// itself: if it is still running when its next fire time comes, that run
// is skipped.
type Scheduler struct {
	Out    *Output
	Exec   Executor      // defaults to RunOnce
	Jitter time.Duration // random delay for tasks without jitter= of their own
	Grace  time.Duration // how long Shutdown waits for running tasks

	// PollInterval is how often Run checks its Source for changes.
	PollInterval time.Duration

	rand func(time.Duration) time.Duration

	mu       sync.Mutex
	entries  []*entry
	state    map[string]*taskState // by task name
	inFlight int                   // runs started and not yet finished
	wg       sync.WaitGroup        // one per run in flight
	stopCtx  context.Context       // done once scheduling has stopped
	stop     context.CancelFunc
	killCtx  context.Context // done when running tasks must die
	kill     context.CancelFunc
}

type entry struct {
	task *taskfile.Task
	next time.Time
}

type taskState struct {
	running bool
	runs    int
}

// NewScheduler returns a scheduler that writes to out.
func NewScheduler(out *Output) *Scheduler {
	s := &Scheduler{
		Out:          out,
		PollInterval: 2 * time.Second,
		rand:         func(d time.Duration) time.Duration { return rand.N(d) },
		state:        make(map[string]*taskState),
	}
	s.Exec = func(ctx context.Context, t *taskfile.Task, run int) error {
		return RunOnce(ctx, t, run, s.Out)
	}
	s.stopCtx, s.stop = context.WithCancel(context.Background())
	s.killCtx, s.kill = context.WithCancel(context.Background())
	return s
}

// Changes summarizes what a Load changed.
type Changes struct {
	Added, Changed, Removed int
}

func (c Changes) String() string {
	var parts []string
	if c.Added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", c.Added))
	}
	if c.Changed > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", c.Changed))
	}
	if c.Removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", c.Removed))
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// Load replaces the scheduled tasks. Each task is matched to the previous
// one it replaces (see match): one whose schedule is unchanged keeps its
// next fire time, so editing the file does not restart every interval,
// while new and rescheduled tasks count from now. Runs in flight are left
// alone, and a task that is still running keeps being skipped until it
// finishes, even if it now goes by another name.
func (s *Scheduler) Load(tasks []*taskfile.Task, now time.Time) Changes {
	s.mu.Lock()
	defer s.mu.Unlock()
	prevs := s.match(tasks)
	ch := Changes{Removed: len(s.entries)}
	entries := make([]*entry, 0, len(tasks))
	moved := make(map[string]*taskState) // new name -> state of a renamed task
	for i, t := range tasks {
		e := &entry{task: t}
		prev := prevs[i]
		switch {
		case prev == nil:
			ch.Added++
		case !sameTask(prev.task, t):
			ch.Changed++
		}
		if prev != nil {
			ch.Removed--
			if prev.task.Name != t.Name {
				moved[t.Name] = s.state[prev.task.Name]
				delete(s.state, prev.task.Name)
			}
		}
		if prev != nil && prev.task.Schedule.String() == t.Schedule.String() {
			e.next = prev.next
		} else if e.next = t.Schedule.Next(now); e.next.IsZero() {
			s.Out.Eventf(t.Name, "never fires")
		}
		entries = append(entries, e)
	}
	// A renamed task takes its run count and any run in flight along.
	for name, st := range moved {
		if st != nil {
			s.state[name] = st
		} else {
			delete(s.state, name)
		}
	}
	s.entries = entries
	return ch
}

// match returns, for each of tasks, the current entry it replaces, or nil
// for a new task. That is the entry of the same name, except that an
// unnamed task that did not change follows its line: unnamed tasks are
// named after their program by position (job, job-2), so adding a line
// above one can shift its name onto another task.
func (s *Scheduler) match(tasks []*taskfile.Task) []*entry {
	byName := make(map[string]*entry, len(s.entries))
	for _, e := range s.entries {
		byName[e.task.Name] = e
	}
	prevs := make([]*entry, len(tasks))
	taken := make(map[*entry]bool)
	// Unchanged tasks under the same name first, then unchanged unnamed
	// tasks under another name, then the rest by name.
	for i, t := range tasks {
		if e := byName[t.Name]; e != nil && sameTask(e.task, t) {
			prevs[i], taken[e] = e, true
		}
	}
	for i, t := range tasks {
		for _, e := range s.entries {
			if prevs[i] == nil && !taken[e] && t.AutoName && e.task.AutoName && sameTask(e.task, t) {
				prevs[i], taken[e] = e, true
			}
		}
	}
	for i, t := range tasks {
		if e := byName[t.Name]; prevs[i] == nil && e != nil && !taken[e] {
			prevs[i], taken[e] = e, true
		}
	}
	return prevs
}

func sameTask(a, b *taskfile.Task) bool {
	return a.Schedule.String() == b.Schedule.String() &&
		a.Command == b.Command &&
		a.Dir == b.Dir &&
		a.Timeout == b.Timeout &&
		a.Jitter == b.Jitter &&
		a.AllowFail == b.AllowFail
}

// Run fires tasks as they come due until ctx is cancelled, then shuts
// down gracefully. If src is not nil it is polled every PollInterval and
// the tasks are reloaded when it changes; if the new tasks do not load,
// the errors are logged and the current schedule stays in place.
func (s *Scheduler) Run(ctx context.Context, src Source) {
	var poll <-chan time.Time
	if src != nil {
		ticker := time.NewTicker(s.PollInterval)
		defer ticker.Stop()
		poll = ticker.C
	}
	for {
		now := time.Now()
		wait := maxWait
		if next := s.tick(now); !next.IsZero() && next.Sub(now) < wait {
			wait = next.Sub(now)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.Shutdown()
			return
		case <-poll:
			timer.Stop()
			s.reload(src)
		case <-timer.C:
		}
	}
}

func (s *Scheduler) reload(src Source) {
	if !src.Changed() {
		return
	}
	tasks, err := src.Load()
	if err != nil {
		var list taskfile.ErrorList
		if errors.As(err, &list) {
			for _, e := range list {
				s.Out.Eventf("cronlite", "%s:%d: %s", src.Name(), e.Line, e.Msg)
			}
		} else {
			s.Out.Eventf("cronlite", "%v", err)
		}
		s.Out.Eventf("cronlite", "%s not reloaded; keeping the previous schedule", src.Name())
		return
	}
	ch := s.Load(tasks, time.Now())
	s.Out.Eventf("cronlite", "reloaded %s: %d %s (%s)", src.Name(), len(tasks), plural(len(tasks), "task", "tasks"), ch)
}

// tick fires every task that is due at now and returns the earliest
// upcoming fire time, or the zero time if nothing is scheduled.
func (s *Scheduler) tick(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	var soonest time.Time
	for _, e := range s.entries {
		if e.next.IsZero() {
			continue
		}
		if !e.next.After(now) {
			s.fire(e.task)
			e.next = advance(e.task.Schedule, e.next, now)
			if e.next.IsZero() {
				s.Out.Eventf(e.task.Name, "never fires again")
				continue
			}
		}
		if soonest.IsZero() || e.next.Before(soonest) {
			soonest = e.next
		}
	}
	return soonest
}

// advance returns the fire time after prev. Runs missed while cronlite was
// not looking (a suspended laptop, a long pause) are dropped rather than
// fired in a burst.
func advance(sched schedule.Schedule, prev, now time.Time) time.Time {
	next := sched.Next(prev)
	if !next.IsZero() && !next.After(now) {
		next = sched.Next(now)
	}
	return next
}

// fire starts a run of t unless one is already in flight. s.mu is held.
func (s *Scheduler) fire(t *taskfile.Task) {
	if s.stopCtx.Err() != nil {
		return
	}
	st := s.state[t.Name]
	if st == nil {
		st = &taskState{}
		s.state[t.Name] = st
	}
	if st.running {
		s.Out.Eventf(t.Name, "skipped: still running")
		return
	}
	st.running = true
	st.runs++
	run := st.runs
	delay := s.jitter(t)
	s.inFlight++
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			st.running = false
			s.inFlight--
			s.mu.Unlock()
		}()
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-s.stopCtx.Done():
				timer.Stop()
				return
			}
		}
		_ = s.Exec(s.killCtx, t, run)
	}()
}

func (s *Scheduler) jitter(t *taskfile.Task) time.Duration {
	limit := s.Jitter
	if t.Jitter > 0 {
		limit = t.Jitter
	}
	if limit <= 0 {
		return 0
	}
	return s.rand(limit)
}

// running returns the number of runs in flight.
func (s *Scheduler) running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight
}

// Shutdown stops scheduling new runs, waits up to Grace for running tasks
// to finish, and then kills whatever is left. Runs still waiting out their
// jitter are dropped.
func (s *Scheduler) Shutdown() {
	s.mu.Lock()
	s.stop()
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	n := s.running()
	if n > 0 && s.Grace > 0 {
		s.Out.Eventf("cronlite", "waiting up to %s for %d running %s",
			schedule.FormatDuration(s.Grace), n, plural(n, "task", "tasks"))
		timer := time.NewTimer(s.Grace)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
		case <-s.killCtx.Done():
		}
	}
	select {
	case <-done:
		return
	default:
	}
	if n = s.running(); n > 0 {
		s.Out.Eventf("cronlite", "killing %d running %s", n, plural(n, "task", "tasks"))
	}
	s.kill()
	<-done
}

// Kill kills all running tasks immediately. It is safe to call while
// Shutdown is waiting.
func (s *Scheduler) Kill() { s.kill() }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
