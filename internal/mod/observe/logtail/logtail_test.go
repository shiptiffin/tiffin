package logtail

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTailRotationAndResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	var got []string
	var saved Position
	var have bool
	mk := func() *Tailer {
		return &Tailer{Path: path, FromStart: true,
			Load: func() (Position, bool) { return saved, have },
			Save: func(p Position) { saved, have = p, true },
			Line: func(l []byte) { got = append(got, string(l)) }}
	}
	write := func(s string) {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString(s)
		f.Close()
	}
	tl := mk()
	tl.Step() // no file yet
	write("one\ntw")
	tl.Step()
	write("o\nthree\n")
	tl.Step()
	if !reflect.DeepEqual(got, []string{"one", "two", "three"}) {
		t.Fatalf("got %q", got)
	}
	// Rotate: rename, write to old fd is gone; new file appears.
	write("four\n")
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	write("five\n")
	tl.Step()
	if !reflect.DeepEqual(got, []string{"one", "two", "three", "four", "five"}) {
		t.Fatalf("after rotation got %q", got)
	}
	tl.close()
	// Restart resumes from the saved position.
	write("six\n")
	got = nil
	tl = mk()
	tl.Step()
	if !reflect.DeepEqual(got, []string{"six"}) {
		t.Fatalf("after restart got %q", got)
	}
	// Truncation starts over.
	os.WriteFile(path, []byte("x\n"), 0o644)
	got = nil
	tl.Step()
	if !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("after truncate got %q", got)
	}
}
