// Package logtail follows append-only log files the way `tail -F` does:
// it survives rotation (rename + new file) and truncation, and remembers
// where it stopped so a restart resumes instead of re-reading or skipping.
package logtail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// Position is where a tailer stopped in a file.
type Position struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

// Tailer follows one path.
type Tailer struct {
	Path string
	// Start is where to begin when there is no saved position for the
	// current file: true = from the beginning, false = from the end.
	FromStart bool
	// Load and Save persist the position (optional).
	Load func() (Position, bool)
	Save func(Position)
	// Line receives every complete line (without the newline). The slice is
	// only valid during the call.
	Line func(line []byte)
	// Poll is how often to check for new data (default 1s).
	Poll time.Duration
	// MaxLine caps a line; longer lines are cut (default 256 KiB).
	MaxLine int

	f       *os.File
	inode   uint64
	offset  int64
	partial []byte
	saved   Position
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// Run follows the file until ctx ends.
func (t *Tailer) Run(ctx context.Context) {
	poll := t.Poll
	if poll == 0 {
		poll = time.Second
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	defer t.close()
	for {
		t.Step()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Step reads whatever is new once. Exposed for tests.
func (t *Tailer) Step() {
	if t.f == nil {
		t.open()
		if t.f == nil {
			return
		}
	}
	t.drain()
	// Rotated or truncated?
	fi, err := os.Stat(t.Path)
	switch {
	case err != nil:
		// The path is gone (rotated, not yet recreated): keep the old fd.
	case inodeOf(fi) != t.inode:
		t.drain() // finish the old file
		t.close()
		t.open()
		if t.f != nil {
			t.drain()
		}
	case fi.Size() < t.offset:
		// Truncated in place: start over.
		_, _ = t.f.Seek(0, io.SeekStart)
		t.offset, t.partial = 0, nil
		t.drain()
	}
	t.persist()
}

func (t *Tailer) open() {
	f, err := os.Open(t.Path)
	if err != nil {
		return
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return
	}
	t.f, t.inode, t.partial = f, inodeOf(fi), nil
	t.offset = 0
	if t.Load != nil {
		if pos, ok := t.Load(); ok && pos.Inode == t.inode && pos.Offset <= fi.Size() {
			t.offset = pos.Offset
		} else if !t.FromStart && !ok {
			t.offset = fi.Size()
		}
	} else if !t.FromStart {
		t.offset = fi.Size()
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		t.offset = 0
	}
}

func (t *Tailer) close() {
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}

func (t *Tailer) drain() {
	if t.f == nil {
		return
	}
	// Nothing new (the common case for an idle app): no read buffer needed.
	if fi, err := t.f.Stat(); err == nil && fi.Size() <= t.offset {
		return
	}
	max := t.MaxLine
	if max == 0 {
		max = 256 << 10
	}
	r := bufio.NewReaderSize(t.f, 64<<10)
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 {
			t.offset += int64(len(chunk))
			if err == nil {
				line := chunk[:len(chunk)-1]
				if len(t.partial) > 0 {
					t.partial = append(t.partial, line...)
					line = t.partial
				}
				line = bytes.TrimSuffix(line, []byte{'\r'})
				if len(line) > max {
					line = line[:max]
				}
				if len(line) > 0 && t.Line != nil {
					t.Line(line)
				}
				t.partial = t.partial[:0]
				continue
			}
			if len(t.partial)+len(chunk) <= max {
				t.partial = append(t.partial, chunk...)
			}
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return
		}
	}
}

func (t *Tailer) persist() {
	if t.Save == nil {
		return
	}
	// Only whole lines count: a restart re-reads a partial line.
	pos := Position{Inode: t.inode, Offset: t.offset - int64(len(t.partial))}
	if pos != t.saved {
		t.saved = pos
		t.Save(pos)
	}
}
