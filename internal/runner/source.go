package runner

import (
	"os"

	"github.com/dan2756/cronlite/internal/taskfile"
)

// A Source supplies tasks to a running Scheduler and says when they have
// changed.
type Source interface {
	// Name identifies the source in log messages.
	Name() string
	// Changed reports whether the tasks should be loaded again.
	Changed() bool
	// Load returns the current tasks.
	Load() ([]*taskfile.Task, error)
}

// FileSource watches a task file by polling its modification time and
// size. A change is reported only once the file has stopped changing for
// one poll, so a reload never sees an editor's half-written save.
type FileSource struct {
	path string

	loaded  fileStamp // what the scheduler last saw
	pending fileStamp // a change waiting to settle
}

type fileStamp struct {
	mod     int64 // UnixNano
	size    int64
	missing bool
}

// NewFileSource returns a source for the task file at path, taking the
// file as it is now as already loaded.
func NewFileSource(path string) *FileSource {
	f := &FileSource{path: path}
	f.loaded = f.stamp()
	f.pending = f.loaded
	return f
}

func (f *FileSource) stamp() fileStamp {
	fi, err := os.Stat(f.path)
	if err != nil {
		return fileStamp{missing: true}
	}
	return fileStamp{mod: fi.ModTime().UnixNano(), size: fi.Size()}
}

func (f *FileSource) Name() string { return f.path }

func (f *FileSource) Changed() bool {
	cur := f.stamp()
	if cur == f.loaded {
		f.pending = cur
		return false
	}
	if cur != f.pending {
		f.pending = cur // still changing; look again next poll
		return false
	}
	f.loaded = cur
	return true
}

func (f *FileSource) Load() ([]*taskfile.Task, error) {
	return taskfile.ParseFile(f.path)
}
