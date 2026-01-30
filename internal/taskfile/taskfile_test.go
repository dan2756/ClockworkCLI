package taskfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dan2756/cronlite/internal/schedule"
)

func TestParseEvery(t *testing.T) {
	src := `# comments and blank lines are fine

every 5m            ./scripts/sync.sh
	every 1h30m   rsync -a ~/notes/ backup:/notes/
every 2d    echo "two days"   # trailing text belongs to the command
`
	tasks, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, cmd string
		line      int
		every     time.Duration
	}{
		{"sync", "./scripts/sync.sh", 3, 5 * time.Minute},
		{"rsync", "rsync -a ~/notes/ backup:/notes/", 4, 90 * time.Minute},
		{"echo", `echo "two days"   # trailing text belongs to the command`, 5, 48 * time.Hour},
	}
	if len(tasks) != len(want) {
		t.Fatalf("got %d tasks, want %d", len(tasks), len(want))
	}
	for i, w := range want {
		got := tasks[i]
		if got.Name != w.name || got.Command != w.cmd || got.Line != w.line {
			t.Errorf("task %d = {%q %q line %d}, want {%q %q line %d}",
				i, got.Name, got.Command, got.Line, w.name, w.cmd, w.line)
		}
		if e, ok := got.Schedule.(schedule.Every); !ok || e.Interval != w.every {
			t.Errorf("task %d schedule = %v, want every %v", i, got.Schedule, w.every)
		}
	}
}

func TestParseErrors(t *testing.T) {
	src := `every 5m ./ok.sh
every
every 5x ./bad.sh
every 0s ./zero.sh
every 10s
hourly ./what.sh
every "5m ./unterminated.sh
`
	tasks, err := Parse(strings.NewReader(src))
	var list ErrorList
	if !errors.As(err, &list) {
		t.Fatalf("err = %v, want ErrorList", err)
	}
	if len(tasks) != 1 {
		t.Errorf("got %d valid tasks, want 1", len(tasks))
	}
	want := map[int]string{
		2: "missing interval",
		3: `invalid duration "5x"`,
		4: "must be positive",
		5: "missing command",
		6: `unknown schedule "hourly"`,
		7: "unterminated",
	}
	if len(list) != len(want) {
		t.Fatalf("got %d errors, want %d: %v", len(list), len(want), list)
	}
	for _, e := range list {
		sub, ok := want[e.Line]
		if !ok {
			t.Errorf("unexpected error on line %d: %s", e.Line, e.Msg)
			continue
		}
		if !strings.Contains(e.Msg, sub) {
			t.Errorf("line %d: error %q does not mention %q", e.Line, e.Msg, sub)
		}
	}
}

func TestDefaultNames(t *testing.T) {
	src := `every 1m ./scripts/sync.sh
every 1m /usr/bin/env python3 job.py
every 1m FOO=bar BAZ=1 ./backup.tar.sh --all
every 1m ./scripts/sync.sh --again
every 1m "my script.sh"
every 1m ./sync.py
`
	tasks, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sync", "env", "backup.tar", "sync-2", "my-script", "sync-3"}
	for i, w := range want {
		if tasks[i].Name != w {
			t.Errorf("task %d name = %q, want %q", i, tasks[i].Name, w)
		}
	}
}

func TestParseFileResolvesDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", "/home/someone")
	path := filepath.Join(dir, "tasks")
	src := `every 1m true
every 1m dir=scripts true
every 1m dir=/srv/app true
every 1m dir=~/notes true
every 1m dir="../with space" true
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		dir,
		filepath.Join(dir, "scripts"),
		"/srv/app",
		"/home/someone/notes",
		filepath.Join(filepath.Dir(dir), "with space"),
	}
	for i, w := range want {
		if tasks[i].Dir != w {
			t.Errorf("task %d Dir = %q, want %q", i, tasks[i].Dir, w)
		}
	}
}

func TestParseOptions(t *testing.T) {
	src := `every 10s  timeout=5s  name=ping   curl -fsS https://example.com/health
at 03:00 weekdays jitter=10m onfail=continue ./backup.sh
cron "0 * * * *" dir="/tmp/a b" FOO=bar ./env.sh
every 1m LANG=C ./run.sh name=x
`
	tasks, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	ping := tasks[0]
	if ping.Name != "ping" || ping.Timeout != 5*time.Second || ping.Command != "curl -fsS https://example.com/health" {
		t.Errorf("ping = %+v", ping)
	}
	backup := tasks[1]
	if backup.Jitter != 10*time.Minute || !backup.AllowFail || backup.Command != "./backup.sh" || backup.Name != "backup" {
		t.Errorf("backup = %+v", backup)
	}
	if env := tasks[2]; env.Dir != "/tmp/a b" || env.Command != "FOO=bar ./env.sh" || env.Name != "env" {
		t.Errorf("env = %+v", env)
	}
	// Options must come before the command; later words belong to it.
	if lang := tasks[3]; lang.Command != "LANG=C ./run.sh name=x" || lang.Name != "run" {
		t.Errorf("lang = %+v", lang)
	}
}

func TestParseOptionErrors(t *testing.T) {
	src := `every 10s timout=5s ./x.sh
every 10s timeout=soon ./x.sh
every 10s timeout=0s ./x.sh
every 10s jitter=-1s ./x.sh
every 10s name= ./x.sh
every 10s name="a b" ./x.sh
every 10s onfail=retry ./x.sh
every 10s dir= ./x.sh
every 10s name=a name=b ./x.sh
every 10s name=ok timeout=1s
`
	_, err := Parse(strings.NewReader(src))
	var list ErrorList
	if !errors.As(err, &list) {
		t.Fatalf("err = %v, want ErrorList", err)
	}
	want := []string{
		`unknown option "timout" (want timeout, name, jitter, dir, onfail)`,
		`timeout: invalid duration "soon"`,
		"timeout: must be positive",
		"jitter: must be positive",
		`name: "" may only contain`,
		`name: "a b" may only contain`,
		`onfail: unknown value "retry"`,
		"dir: missing directory",
		"name= given twice",
		"missing command",
	}
	if len(list) != len(want) {
		t.Fatalf("got %d errors, want %d: %v", len(list), len(want), list)
	}
	for i, w := range want {
		if list[i].Line != i+1 || !strings.Contains(list[i].Msg, w) {
			t.Errorf("error %d = line %d %q, want line %d containing %q", i, list[i].Line, list[i].Msg, i+1, w)
		}
	}
}

func TestDuplicateNames(t *testing.T) {
	src := `every 1m ./sync.sh
every 1m name=sync ./other.sh
every 1m name=ping true
every 2m name=ping false
`
	tasks, err := Parse(strings.NewReader(src))
	var list ErrorList
	if !errors.As(err, &list) || len(list) != 1 {
		t.Fatalf("err = %v, want one error", err)
	}
	if list[0].Line != 4 || !strings.Contains(list[0].Msg, `duplicate task name "ping" (also on line 3)`) {
		t.Errorf("error = %v", list[0])
	}
	// An explicit name wins; the derived one moves aside.
	if tasks[0].Name != "sync-2" || tasks[1].Name != "sync" {
		t.Errorf("names = %q, %q", tasks[0].Name, tasks[1].Name)
	}
}

func TestParseCron(t *testing.T) {
	src := `cron "*/15 9-17 * * 1-5"  ./check-queue.sh
cron '0 3 * * sun' ./weekly.sh
cron 0 12 1 * *   echo "first of the month"
`
	tasks, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ sched, cmd string }{
		{`cron "*/15 9-17 * * 1-5"`, "./check-queue.sh"},
		{`cron "0 3 * * sun"`, "./weekly.sh"},
		{`cron "0 12 1 * *"`, `echo "first of the month"`},
	}
	for i, w := range want {
		if got := tasks[i].Schedule.String(); got != w.sched {
			t.Errorf("task %d schedule = %s, want %s", i, got, w.sched)
		}
		if tasks[i].Command != w.cmd {
			t.Errorf("task %d command = %q, want %q", i, tasks[i].Command, w.cmd)
		}
	}
}

func TestParseCronErrors(t *testing.T) {
	src := `cron
cron "61 * * * *" ./x.sh
cron "* * * *" ./x.sh
cron "0 0 30 2 *" ./never.sh
cron "* * * * *
`
	_, err := Parse(strings.NewReader(src))
	var list ErrorList
	if !errors.As(err, &list) {
		t.Fatalf("err = %v, want ErrorList", err)
	}
	want := []string{
		"cron: missing expression",
		"cron: minute: 61 out of range 0-59",
		"cron: want 5 fields",
		"cron: day-of-month 30 never occurs in month 2",
		"cron: unterminated",
	}
	if len(list) != len(want) {
		t.Fatalf("got %d errors, want %d: %v", len(list), len(want), list)
	}
	for i, w := range want {
		if list[i].Line != i+1 || !strings.Contains(list[i].Msg, w) {
			t.Errorf("error %d = line %d %q, want line %d containing %q", i, list[i].Line, list[i].Msg, i+1, w)
		}
	}
}

func TestParseAt(t *testing.T) {
	src := `at 09:00            ./standup-reminder.sh
at 09:00,13:30 weekdays  say "stretch"
at 18:00 mon,wed,fri   ./gym.sh
at 7:30 Weekends sleep 3600
at 12:00 saybye
`
	tasks, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ sched, cmd string }{
		{"at 09:00", "./standup-reminder.sh"},
		{"at 09:00,13:30 weekdays", `say "stretch"`},
		{"at 18:00 mon,wed,fri", "./gym.sh"},
		{"at 07:30 weekends", "sleep 3600"},
		{"at 12:00", "saybye"},
	}
	for i, w := range want {
		if got := tasks[i].Schedule.String(); got != w.sched {
			t.Errorf("task %d schedule = %s, want %s", i, got, w.sched)
		}
		if tasks[i].Command != w.cmd {
			t.Errorf("task %d command = %q, want %q", i, tasks[i].Command, w.cmd)
		}
	}
}

func TestParseAtErrors(t *testing.T) {
	src := `at
at 25:00 ./late.sh
at 9 ./nine.sh
at 09:00 weekdays
`
	_, err := Parse(strings.NewReader(src))
	var list ErrorList
	if !errors.As(err, &list) {
		t.Fatalf("err = %v, want ErrorList", err)
	}
	want := []string{
		"at: missing time",
		`at: invalid time "25:00"`,
		`at: invalid time "9"`,
		"missing command",
	}
	if len(list) != len(want) {
		t.Fatalf("got %d errors, want %d: %v", len(list), len(want), list)
	}
	for i, w := range want {
		if list[i].Line != i+1 || !strings.Contains(list[i].Msg, w) {
			t.Errorf("error %d = line %d %q, want line %d containing %q", i, list[i].Line, list[i].Msg, i+1, w)
		}
	}
}
