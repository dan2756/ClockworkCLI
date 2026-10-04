// Package taskfile parses cronlite task files.
//
// A task file has one task per line: a schedule followed by a shell
// command. Blank lines and lines starting with # are ignored.
//
//	every 5m                  ./scripts/sync.sh
//	every 1h30m               rsync -a ~/notes/ backup:/notes/
//	at 09:00,13:30 weekdays   say "stretch"
//	cron "*/15 9-17 * * 1-5"  ./check-queue.sh
//
// Options may sit between the schedule and the command:
//
//	every 10s timeout=5s name=ping curl -fsS https://example.com/health
package taskfile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dan2756/cronlite/internal/schedule"
)

// A Task is one line of a task file.
type Task struct {
	Name     string
	Line     int
	Schedule schedule.Schedule
	Command  string
	Dir      string        // working directory; empty means the current directory
	Timeout  time.Duration // kill the run after this long; 0 means never
	Jitter   time.Duration // delay each run by a random amount up to this
	// AllowFail is set by onfail=continue: a failed run is reported but
	// not treated as an error.
	AllowFail bool
	// AutoName is set when Name was derived from the command rather than
	// given with name=. Such a name depends on the lines above it (sync,
	// sync-2), so it can change when the file is edited.
	AutoName bool
}

// Options lists the option keys accepted between the schedule and the
// command.
var Options = []string{"timeout", "name", "jitter", "dir", "onfail"}

// An Error is a problem on a specific line of a task file.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// ErrorList collects every problem found in a task file.
type ErrorList []*Error

func (l ErrorList) Error() string {
	switch len(l) {
	case 0:
		return "no errors"
	case 1:
		return l[0].Error()
	}
	return fmt.Sprintf("%s (and %d more)", l[0].Error(), len(l)-1)
}

// Parse reads a task file. If any line is invalid it returns an ErrorList
// describing every bad line, along with the tasks that did parse.
func Parse(r io.Reader) ([]*Task, error) {
	var (
		tasks []*Task
		errs  ErrorList
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		t, err := parseLine(line)
		if err != nil {
			errs = append(errs, &Error{Line: n, Msg: err.Error()})
			continue
		}
		t.Line = n
		tasks = append(tasks, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if dups := assignNames(tasks); len(dups) > 0 {
		errs = append(errs, dups...)
		slices.SortStableFunc(errs, func(a, b *Error) int { return a.Line - b.Line })
	}
	if len(errs) > 0 {
		return tasks, errs
	}
	return tasks, nil
}

// ParseFile reads the task file at path. Tasks run in the directory that
// contains the file unless they set dir=, which is itself relative to that
// directory and may start with ~.
func ParseFile(name string) ([]*Task, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tasks, err := Parse(f)
	base, absErr := filepath.Abs(filepath.Dir(name))
	if absErr != nil {
		return nil, absErr
	}
	for _, t := range tasks {
		t.Dir = resolveDir(base, t.Dir)
	}
	return tasks, err
}

func resolveDir(base, dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[1:])
		}
	}
	if dir == "" {
		return base
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	return filepath.Clean(dir)
}

func parseLine(line string) (*Task, error) {
	lx := &lexer{s: line}
	t := &Task{}
	if err := parseSchedule(lx, t); err != nil {
		return nil, err
	}
	if err := parseOptions(lx, t); err != nil {
		return nil, err
	}
	t.Command = lx.rest()
	if t.Command == "" {
		return nil, fmt.Errorf("missing command")
	}
	return t, nil
}

// ParseSchedule parses just the schedule part of a task line, such as
// "every 5m", "at 09:00,13:30 weekdays" or `cron "*/15 9-17 * * 1-5"`.
func ParseSchedule(s string) (schedule.Schedule, error) {
	lx := &lexer{s: strings.TrimSpace(s)}
	t := &Task{}
	if err := parseSchedule(lx, t); err != nil {
		return nil, err
	}
	if rest := lx.rest(); rest != "" {
		return nil, fmt.Errorf("unexpected %q after %s", rest, t.Schedule)
	}
	return t.Schedule, nil
}

func parseSchedule(lx *lexer, t *Task) error {
	kw, err := lx.word()
	if err != nil {
		return err
	}
	switch kw {
	case "every":
		arg, err := lx.word()
		if err != nil {
			return err
		}
		if arg == "" {
			return fmt.Errorf("every: missing interval")
		}
		d, err := schedule.ParseDuration(arg)
		if err != nil {
			return fmt.Errorf("every: %v", err)
		}
		if d <= 0 {
			return fmt.Errorf("every: interval must be positive")
		}
		t.Schedule = schedule.Every{Interval: d}
	case "at":
		arg, err := lx.word()
		if err != nil {
			return err
		}
		if arg == "" {
			return fmt.Errorf("at: missing time")
		}
		times, err := schedule.ParseTimes(arg)
		if err != nil {
			return fmt.Errorf("at: %v", err)
		}
		a := &schedule.At{Times: times, Days: schedule.AllDays}
		// An optional day filter may follow; anything else starts the command.
		save := lx.pos
		if w, err := lx.word(); err == nil && w != "" {
			days, err := schedule.ParseDays(w)
			switch {
			case err == nil:
				a.Days = days
				save = lx.pos
			case meantAsDays(w):
				return fmt.Errorf("at: %v (use e.g. weekdays, weekends, mon,wed,fri or mon-thu)", err)
			}
		}
		lx.pos = save
		t.Schedule = a
	case "cron":
		expr, err := lx.cronExpr()
		if err != nil {
			return err
		}
		c, err := schedule.ParseCron(expr)
		if err != nil {
			return fmt.Errorf("cron: %v", err)
		}
		t.Schedule = c
	default:
		return fmt.Errorf("unknown schedule %q (want every, at or cron)", kw)
	}
	return nil
}

// meantAsDays reports whether a word after an at time that is not a valid
// day filter was still meant as one, rather than as the start of the
// command: a list ("mon,wed,fryday"), "weekday" or "weekend", or day names
// cut short ("tues", "mon-thurs").
func meantAsDays(w string) bool {
	w = strings.ToLower(w)
	if strings.ContainsFunc(w, func(r rune) bool { return (r < 'a' || r > 'z') && r != ',' && r != '-' }) {
		return false
	}
	if strings.Contains(w, ",") || w == "weekday" || w == "weekend" {
		return true
	}
	for _, part := range strings.Split(w, "-") {
		if !isDayPrefix(part) {
			return false
		}
	}
	return true
}

// isDayPrefix reports whether s is the start of a day name, at least three
// letters long, such as "wed", "thurs" or "saturday".
func isDayPrefix(s string) bool {
	if len(s) < 3 {
		return false
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.HasPrefix(strings.ToLower(d.String()), s) {
			return true
		}
	}
	return false
}

// parseOptions reads key=value options up to the first word that is not
// one. Only lowercase keys are options, so a command may still start with
// an environment assignment such as FOO=bar.
func parseOptions(lx *lexer, t *Task) error {
	seen := make(map[string]bool)
	for {
		save := lx.pos
		w, err := lx.word()
		if err != nil {
			return err
		}
		key, val, ok := strings.Cut(w, "=")
		if !ok || !isLowerWord(key) {
			lx.pos = save
			return nil
		}
		if !slices.Contains(Options, key) {
			return fmt.Errorf("unknown option %q (want %s)", key, strings.Join(Options, ", "))
		}
		if seen[key] {
			return fmt.Errorf("%s= given twice", key)
		}
		seen[key] = true
		switch key {
		case "timeout", "jitter":
			d, err := schedule.ParseDuration(val)
			if err != nil {
				return fmt.Errorf("%s: %v", key, err)
			}
			if d <= 0 {
				return fmt.Errorf("%s: must be positive", key)
			}
			if key == "timeout" {
				t.Timeout = d
			} else {
				t.Jitter = d
			}
		case "name":
			if val == "" || strings.IndexFunc(val, func(r rune) bool { return !isNameRune(r) }) >= 0 {
				return fmt.Errorf("name: %q may only contain letters, digits, '.', '_' and '-'", val)
			}
			t.Name = val
		case "dir":
			if val == "" {
				return fmt.Errorf("dir: missing directory")
			}
			t.Dir = val
		case "onfail":
			if val != "continue" {
				return fmt.Errorf(`onfail: unknown value %q (only "continue" is supported)`, val)
			}
			t.AllowFail = true
		}
	}
}

func isLowerWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// assignNames checks that explicit names are unique and gives every other
// task a unique name derived from its command.
func assignNames(tasks []*Task) []*Error {
	var errs []*Error
	taken := make(map[string]int) // name -> line
	for _, t := range tasks {
		if t.Name == "" {
			continue
		}
		if line, dup := taken[t.Name]; dup {
			errs = append(errs, &Error{Line: t.Line, Msg: fmt.Sprintf("duplicate task name %q (also on line %d)", t.Name, line)})
			continue
		}
		taken[t.Name] = t.Line
	}
	for _, t := range tasks {
		if t.Name != "" {
			continue
		}
		base := commandName(t.Command)
		name := base
		for n := 2; taken[name] != 0; n++ {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		taken[name] = t.Line
		t.Name, t.AutoName = name, true
	}
	return errs
}

// commandName derives a short name from a command line: the base name of
// the program without its extension, so "./scripts/sync.sh --all" becomes
// "sync".
func commandName(cmd string) string {
	var prog string
	lx := &lexer{s: cmd}
	for {
		w, err := lx.word()
		if err != nil || w == "" {
			break
		}
		if !isEnvAssignment(w) {
			prog = w
			break
		}
	}
	base := path.Base(prog)
	if ext := path.Ext(base); ext != "" && ext != base {
		base = strings.TrimSuffix(base, ext)
	}
	name := strings.Map(func(r rune) rune {
		if isNameRune(r) {
			return r
		}
		return '-'
	}, base)
	name = strings.Trim(name, "-.")
	if name == "" {
		return "task"
	}
	return name
}

func isEnvAssignment(s string) bool {
	k, _, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return false
	}
	for i, r := range k {
		if r != '_' && !('A' <= r && r <= 'Z') && !('a' <= r && r <= 'z') && !(i > 0 && '0' <= r && r <= '9') {
			return false
		}
	}
	return true
}

func isNameRune(r rune) bool {
	return r == '-' || r == '_' || r == '.' ||
		('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
}

// lexer splits the schedule part of a line into words while keeping the
// command that follows untouched.
type lexer struct {
	s   string
	pos int
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.s) && (l.s[l.pos] == ' ' || l.s[l.pos] == '\t') {
		l.pos++
	}
}

// word returns the next whitespace-separated word, with any single or
// double quotes removed. It returns "" at the end of the line.
func (l *lexer) word() (string, error) {
	l.skipSpace()
	var b strings.Builder
	for l.pos < len(l.s) {
		c := l.s[l.pos]
		switch c {
		case ' ', '\t':
			return b.String(), nil
		case '"', '\'':
			end := strings.IndexByte(l.s[l.pos+1:], c)
			if end < 0 {
				return "", fmt.Errorf("unterminated %c quote", c)
			}
			b.WriteString(l.s[l.pos+1 : l.pos+1+end])
			l.pos += end + 2
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	return b.String(), nil
}

// cronExpr reads a cron expression: one quoted word, a nickname such as
// @daily, or five unquoted fields.
func (l *lexer) cronExpr() (string, error) {
	l.skipSpace()
	if l.pos < len(l.s) && (l.s[l.pos] == '"' || l.s[l.pos] == '\'' || l.s[l.pos] == '@') {
		expr, err := l.word()
		if err != nil {
			return "", fmt.Errorf("cron: %v", err)
		}
		return expr, nil
	}
	fields := make([]string, 0, 5)
	for range 5 {
		w, err := l.word()
		if err != nil {
			return "", fmt.Errorf("cron: %v", err)
		}
		if w == "" {
			break
		}
		fields = append(fields, w)
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("cron: missing expression")
	}
	return strings.Join(fields, " "), nil
}

// rest returns the remainder of the line, verbatim.
func (l *lexer) rest() string {
	return strings.TrimSpace(l.s[l.pos:])
}
