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

func TestParseFileSetsDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks")
	if err := os.WriteFile(path, []byte("every 1m true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Dir != dir {
		t.Errorf("Dir = %q, want %q", tasks[0].Dir, dir)
	}
}
