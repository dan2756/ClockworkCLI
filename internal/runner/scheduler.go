package runner

import (
	"context"
	"math/rand/v2"
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

	rand func(time.Duration) time.Duration

	mu      sync.Mutex
	entries []*entry
	state   map[string]*taskState // by task name
	wg      sync.WaitGroup        // one per run in flight
	stopCtx context.Context       // done once scheduling has stopped
	stop    context.CancelFunc
	killCtx context.Context // done when running tasks must die
	kill    context.CancelFunc
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
		Out:   out,
		rand:  func(d time.Duration) time.Duration { return rand.N(d) },
		state: make(map[string]*taskState),
	}
	s.Exec = func(ctx context.Context, t *taskfile.Task, run int) error {
		return RunOnce(ctx, t, run, s.Out)
	}
	s.stopCtx, s.stop = context.WithCancel(context.Background())
	s.killCtx, s.kill = context.WithCancel(context.Background())
	return s
}

// Load replaces the scheduled tasks, computing each one's next fire time
// from now.
func (s *Scheduler) Load(tasks []*taskfile.Task, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = s.entries[:0]
	for _, t := range tasks {
		e := &entry{task: t, next: t.Schedule.Next(now)}
		if e.next.IsZero() {
			s.Out.Eventf(t.Name, "never fires")
		}
		s.entries = append(s.entries, e)
	}
}

// Run fires tasks as they come due until ctx is cancelled, then shuts
// down gracefully.
func (s *Scheduler) Run(ctx context.Context) {
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
		case <-timer.C:
		}
	}
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
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			st.running = false
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
	n := 0
	for _, st := range s.state {
		if st.running {
			n++
		}
	}
	return n
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
