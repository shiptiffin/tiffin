package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogLine is one line an app instance wrote.
type LogLine struct {
	Time     time.Time `json:"time"`
	Stream   string    `json:"stream" enum:"stdout,stderr"`
	Deploy   string    `json:"deploy"`
	Instance string    `json:"instance" doc:"Container name"`
	Text     string    `json:"text"`
}

// Each container logs to <LogDir>/<project>/<app>/<env>/<deploy>.<serial>.log
// (JSON lines written by nerdctl's json-file driver, rotated at 5 MB, three
// files kept). env is "prod" or "pr-<preview>".
func (r *rt) logFile(project, app, preview, deploy string, serial int) string {
	return filepath.Join(r.envLogDir(project, app, preview), deploy+"."+strconv.Itoa(serial)+".log")
}

func (r *rt) envLogDir(project, app, preview string) string {
	env := "prod"
	if preview != "" {
		env = "pr-" + preview
	}
	return filepath.Join(r.opt.LogDir, project, app, env)
}

type logSource struct {
	path     string // current file; rotated ones are path.1, path.2
	deploy   string
	instance string
}

func (r *rt) logSources(project, app, preview, deploy string) []logSource {
	dir := r.envLogDir(project, app, preview)
	ents, _ := os.ReadDir(dir)
	var out []logSource
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".log") {
			continue
		}
		base := strings.TrimSuffix(name, ".log")
		dep, serial, ok := strings.Cut(base, ".")
		if !ok || (deploy != "" && dep != deploy) {
			continue
		}
		n, _ := strconv.Atoi(serial)
		out = append(out, logSource{path: filepath.Join(dir, name), deploy: dep, instance: containerName(project, app, preview, n)})
	}
	return out
}

type jsonLogLine struct {
	Log    string    `json:"log"`
	Stream string    `json:"stream"`
	Time   time.Time `json:"time"`
}

func parseLine(raw []byte, src logSource) (LogLine, bool) {
	var j jsonLogLine
	if json.Unmarshal(raw, &j) != nil {
		return LogLine{}, false
	}
	return LogLine{Time: j.Time, Stream: j.Stream, Deploy: src.deploy, Instance: src.instance, Text: strings.TrimRight(j.Log, "\n")}, true
}

// readLogs returns lines after since (exclusive), oldest first. With limit,
// the newest limit lines are returned (tail semantics).
func (r *rt) readLogs(project, app, preview, deploy string, since time.Time, limit int) []LogLine {
	var all []LogLine
	for _, src := range r.logSources(project, app, preview, deploy) {
		for _, p := range []string{src.path + ".2", src.path + ".1", src.path} {
			f, err := os.Open(p)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64*1024), 1<<20)
			for sc.Scan() {
				if l, ok := parseLine(sc.Bytes(), src); ok && l.Time.After(since) {
					all = append(all, l)
				}
			}
			f.Close()
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
}

// logFollower tails an app environment's log files.
type logFollower struct {
	r                          *rt
	project, app, preview, dep string
	offsets                    map[string]int64
}

func (r *rt) follow(project, app, preview, deploy string) *logFollower {
	f := &logFollower{r: r, project: project, app: app, preview: preview, dep: deploy, offsets: map[string]int64{}}
	// Start at the end of every current file.
	for _, src := range r.logSources(project, app, preview, deploy) {
		if fi, err := os.Stat(src.path); err == nil {
			f.offsets[src.path] = fi.Size()
		}
	}
	return f
}

// poll returns lines written since the last poll.
func (f *logFollower) poll() []LogLine {
	var out []LogLine
	for _, src := range f.r.logSources(f.project, f.app, f.preview, f.dep) {
		off := f.offsets[src.path]
		fi, err := os.Stat(src.path)
		if err != nil {
			continue
		}
		if fi.Size() < off {
			off = 0 // rotated
		}
		if fi.Size() == off {
			continue
		}
		fh, err := os.Open(src.path)
		if err != nil {
			continue
		}
		_, _ = fh.Seek(off, io.SeekStart)
		data, _ := io.ReadAll(io.LimitReader(fh, 8<<20))
		fh.Close()
		// Only consume complete lines.
		end := bytes.LastIndexByte(data, '\n')
		if end < 0 {
			continue
		}
		f.offsets[src.path] = off + int64(end) + 1
		for _, raw := range bytes.Split(data[:end], []byte{'\n'}) {
			if l, ok := parseLine(raw, src); ok {
				out = append(out, l)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// tailLog returns the last n lines of a container log as plain text.
func tailLog(path string, n int) string {
	var lines []string
	for _, p := range []string{path + ".1", path} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range bytes.Split(bytes.TrimRight(raw, "\n"), []byte{'\n'}) {
			if ll, ok := parseLine(l, logSource{}); ok {
				lines = append(lines, "  "+ll.Text)
			}
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return "  (no output)"
	}
	return strings.Join(lines, "\n")
}

// pruneLogs keeps log files (and build output copies, see buildlog.go) of
// the newest 10 deploys of an environment.
func (r *rt) pruneLogs(project, app, preview string, newestFirst []*Deploy) {
	keep := map[string]bool{}
	for i, d := range newestFirst {
		if i < 10 || d.Status == StatusLive {
			keep[d.ID] = true
		}
	}
	r.pruneBuildLogs(project, app, keep, newestFirst)
	for _, src := range r.logSources(project, app, preview, "") {
		if !keep[src.deploy] {
			for _, p := range []string{src.path, src.path + ".1", src.path + ".2"} {
				_ = os.Remove(p)
			}
		}
	}
}

// readBuildLog returns a deploy's build log from byte offset off.
func (r *rt) readBuildLog(d *Deploy, off int64, maxBytes int64) ([]byte, int64) {
	f, err := os.Open(r.buildLogPath(d))
	if err != nil {
		return nil, off
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, off
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxBytes))
	return data, off + int64(len(data))
}
