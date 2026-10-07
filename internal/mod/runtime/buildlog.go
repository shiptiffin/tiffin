package runtime

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A deploy's build output goes to two places:
//
//   - build.log in its work folder, plain text, exactly as written: the
//     deploy page and `deploys build-log` read it (readBuildLog);
//   - <LogDir>/<project>/<app>/build/<deploy>.log, one JSON line per output
//     line with the time it was written and the environment (prod or
//     pr-<preview>, as the app's own log folders name it). The observe
//     module tails the app log folder and ships these to the log store as
//     source=build (observe.BuildLine reads this format).
//
// "build" never clashes with an environment folder, which are prod and
// pr-<preview>. pruneLogs removes the copies with the app's own logs.

// shipLine is one line of the shipper's copy (observe.BuildLine).
type shipLine struct {
	Time string `json:"time"`
	Env  string `json:"env"`
	Log  string `json:"log"`
}

const buildLogFolder = "build"

// maxShipLine cuts very long output lines in the shipper's copy (build.log
// keeps them whole).
const maxShipLine = 64 << 10

func (r *rt) buildShipDir(project, app string) string {
	return filepath.Join(r.opt.LogDir, project, app, buildLogFolder)
}

func (r *rt) buildShipPath(d *Deploy) string {
	return filepath.Join(r.buildShipDir(d.Project, d.App), d.ID+".log")
}

// buildLog writes a deploy's build output to build.log and the shipper's copy.
type buildLog struct {
	mu   sync.Mutex
	file *os.File
	ship *os.File // nil when the copy cannot be written: the build goes on
	env  string   // prod or pr-<preview>
	part []byte
	now  func() time.Time
}

// openBuildLog opens a deploy's build log for appending.
func (r *rt) openBuildLog(d *Deploy) (*buildLog, error) {
	f, err := os.OpenFile(r.buildLogPath(d), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	b := &buildLog{file: f, env: envDirName(d.Preview), now: time.Now}
	if r.opt.LogDir != "" {
		if err := os.MkdirAll(r.buildShipDir(d.Project, d.App), 0o755); err == nil {
			b.ship, _ = os.OpenFile(r.buildShipPath(d), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		}
	}
	return b, nil
}

// Write appends p to build.log as is, and its complete lines to the copy.
func (b *buildLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.file.Write(p)
	if b.ship != nil && n > 0 {
		b.part = append(b.part, p[:n]...)
		for {
			i := bytes.IndexByte(b.part, '\n')
			if i < 0 {
				break
			}
			b.emit(b.part[:i])
			b.part = b.part[i+1:]
		}
		if len(b.part) > maxShipLine {
			b.emit(b.part)
			b.part = b.part[:0]
		}
		b.part = append([]byte(nil), b.part...)
	}
	return n, err
}

// emit writes one line to the copy: what a terminal would show (the text
// after the last carriage return), blank lines left out.
func (b *buildLog) emit(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if i := bytes.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	if len(line) > maxShipLine {
		line = line[:maxShipLine]
	}
	j, err := json.Marshal(shipLine{Time: b.now().UTC().Format(time.RFC3339Nano), Env: b.env, Log: string(line)})
	if err != nil {
		return
	}
	_, _ = b.ship.Write(append(j, '\n')) // one write per line: appends from several writers never interleave
}

// Close writes out an unfinished last line and closes both files.
func (b *buildLog) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ship != nil {
		if len(b.part) > 0 {
			b.emit(b.part)
			b.part = nil
		}
		b.ship.Close()
		b.ship = nil
	}
	return b.file.Close()
}

var _ io.WriteCloser = (*buildLog)(nil)

// pruneBuildLogs removes the shipper's copies of the deploys not kept.
func (r *rt) pruneBuildLogs(project, app string, keep map[string]bool, deploys []*Deploy) {
	if r.opt.LogDir == "" {
		return
	}
	dir := r.buildShipDir(project, app)
	for _, d := range deploys {
		if !keep[d.ID] {
			_ = os.Remove(filepath.Join(dir, d.ID+".log"))
		}
	}
}
