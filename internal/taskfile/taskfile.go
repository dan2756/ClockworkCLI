// Package taskfile parses cronlite task files.
//
// A task file has one task per line: a schedule followed by a shell
// command. Blank lines and lines starting with # are ignored.
//
//	every 5m      ./scripts/sync.sh
//	every 1h30m   rsync -a ~/notes/ backup:/notes/
package taskfile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dan2756/cronlite/internal/schedule"
)

// A Task is one line of a task file.
type Task struct {
	Name     string
	Line     int
	Schedule schedule.Schedule
	Command  string
	Dir      string // working directory; empty means the current directory
}

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
	assignNames(tasks)
	if len(errs) > 0 {
		return tasks, errs
	}
	return tasks, nil
}

// ParseFile reads the task file at path. Tasks run in the directory that
// contains the file.
func ParseFile(name string) ([]*Task, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tasks, err := Parse(f)
	dir, absErr := filepath.Abs(filepath.Dir(name))
	if absErr != nil {
		return nil, absErr
	}
	for _, t := range tasks {
		t.Dir = dir
	}
	return tasks, err
}

func parseLine(line string) (*Task, error) {
	lx := &lexer{s: line}
	kw, err := lx.word()
	if err != nil {
		return nil, err
	}
	t := &Task{}
	switch kw {
	case "every":
		arg, err := lx.word()
		if err != nil {
			return nil, err
		}
		if arg == "" {
			return nil, fmt.Errorf("every: missing interval")
		}
		d, err := schedule.ParseDuration(arg)
		if err != nil {
			return nil, fmt.Errorf("every: %v", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("every: interval must be positive")
		}
		t.Schedule = schedule.Every{Interval: d}
	default:
		return nil, fmt.Errorf("unknown schedule %q (want every)", kw)
	}
	t.Command = lx.rest()
	if t.Command == "" {
		return nil, fmt.Errorf("missing command")
	}
	return t, nil
}

// assignNames gives every task a unique name derived from its command.
func assignNames(tasks []*Task) {
	seen := make(map[string]int)
	for _, t := range tasks {
		base := commandName(t.Command)
		seen[base]++
		t.Name = base
		if n := seen[base]; n > 1 {
			t.Name = fmt.Sprintf("%s-%d", base, n)
		}
	}
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

// rest returns the remainder of the line, verbatim.
func (l *lexer) rest() string {
	return strings.TrimSpace(l.s[l.pos:])
}
