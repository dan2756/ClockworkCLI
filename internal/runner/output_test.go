package runner

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedOutput() (*Output, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	o := &Output{
		Stdout: &stdout,
		Stderr: &stderr,
		Now:    func() time.Time { return time.Date(2026, 1, 25, 19, 51, 18, 0, time.UTC) },
	}
	return o, &stdout, &stderr
}

func TestLineWriterPrefixesLines(t *testing.T) {
	o, stdout, _ := fixedOutput()
	w := o.lineWriter(o.Stdout, "sync")
	fmt.Fprint(w, "one\ntw")
	fmt.Fprint(w, "o\r\nthree")
	if got := strings.Count(stdout.String(), "\n"); got != 2 {
		t.Fatalf("partial line written early: %q", stdout.String())
	}
	w.Flush()
	want := "2026-01-25 19:51:18 [sync] one\n" +
		"2026-01-25 19:51:18 [sync] two\n" +
		"2026-01-25 19:51:18 [sync] three\n"
	if stdout.String() != want {
		t.Errorf("got\n%s\nwant\n%s", stdout.String(), want)
	}
	w.Flush()
	if stdout.String() != want {
		t.Error("second Flush wrote again")
	}
}

func TestEventfGoesToStderr(t *testing.T) {
	o, stdout, stderr := fixedOutput()
	o.Eventf("ping", "skipped: %s", "still running")
	if stdout.Len() != 0 {
		t.Errorf("event written to stdout: %q", stdout.String())
	}
	if want := "2026-01-25 19:51:18 [ping] -- skipped: still running\n"; stderr.String() != want {
		t.Errorf("got %q, want %q", stderr.String(), want)
	}
}

func TestLineWriterConcurrentTasksDoNotInterleave(t *testing.T) {
	o, stdout, _ := fixedOutput()
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := o.lineWriter(o.Stdout, fmt.Sprintf("t%d", i))
			for range 200 {
				fmt.Fprint(w, "aaaa")
				fmt.Fprint(w, "bbbb\n")
			}
		}()
	}
	wg.Wait()
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if !strings.HasSuffix(line, "] aaaabbbb") {
			t.Fatalf("mangled line %q", line)
		}
	}
}

func TestLineWriterFlushesHugeLines(t *testing.T) {
	o, stdout, _ := fixedOutput()
	w := o.lineWriter(o.Stdout, "big")
	w.Write(bytes.Repeat([]byte("x"), maxLine+10))
	if stdout.Len() == 0 {
		t.Error("oversized partial line was not flushed")
	}
}
