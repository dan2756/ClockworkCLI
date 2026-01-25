// Package runner executes tasks and schedules them.
package runner

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"
)

const timeFormat = "2006-01-02 15:04:05"

// maxLine is the longest partial line buffered before it is written out
// anyway, so a task printing without newlines cannot grow memory forever.
const maxLine = 64 * 1024

// Output writes task output and scheduler events, each line prefixed with
// a timestamp and the task name. It is safe for concurrent use, and lines
// from different tasks never interleave.
type Output struct {
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time // defaults to time.Now

	mu sync.Mutex
}

func (o *Output) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Output) writeLine(w io.Writer, name string, text []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(w, "%s [%s] %s\n", o.now().Format(timeFormat), name, text)
}

// Eventf writes a scheduler message about the named task to Stderr.
// Event lines are marked with "--" to set them apart from task output.
func (o *Output) Eventf(name, format string, args ...any) {
	o.writeLine(o.Stderr, name, []byte("-- "+fmt.Sprintf(format, args...)))
}

// lineWriter returns a writer that prefixes every line written to it.
func (o *Output) lineWriter(w io.Writer, name string) *lineWriter {
	return &lineWriter{out: o, w: w, name: name}
}

type lineWriter struct {
	out  *Output
	w    io.Writer
	name string
	buf  []byte
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	start := 0
	for {
		i := bytes.IndexByte(lw.buf[start:], '\n')
		if i < 0 {
			break
		}
		lw.out.writeLine(lw.w, lw.name, bytes.TrimSuffix(lw.buf[start:start+i], []byte("\r")))
		start += i + 1
	}
	lw.buf = lw.buf[:copy(lw.buf, lw.buf[start:])]
	if len(lw.buf) >= maxLine {
		lw.Flush()
	}
	return len(p), nil
}

// Flush writes out a trailing line that has no newline.
func (lw *lineWriter) Flush() {
	if len(lw.buf) > 0 {
		lw.out.writeLine(lw.w, lw.name, lw.buf)
		lw.buf = lw.buf[:0]
	}
}
