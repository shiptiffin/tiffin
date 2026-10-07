package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// shipLines reads the shipper's copy of a deploy's build output.
func shipLines(t *testing.T, r *rt, d *Deploy) []shipLine {
	t.Helper()
	f, err := os.Open(r.buildShipPath(d))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []shipLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l shipLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("not a JSON line: %q", sc.Text())
		}
		if _, err := time.Parse(time.RFC3339Nano, l.Time); err != nil {
			t.Fatalf("time %q: %v", l.Time, err)
		}
		out = append(out, l)
	}
	return out
}

func TestBuildLogTee(t *testing.T) {
	dir := t.TempDir()
	r := &rt{opt: Options{DataDir: filepath.Join(dir, "runtime"), LogDir: filepath.Join(dir, "logs")}}
	d := &Deploy{ID: "dep_01", Project: "shop", App: "web"}
	os.MkdirAll(r.workDir(d), 0o755)
	b, err := r.openBuildLog(d)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	b.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	chunks := []string{"==> deploy dep_01 of shop/web\n#8 0.1 npm wa", "rn deprecated x\r\n", "\n   \n", "#9 10%\r#9 55%\r#9 done\n", "no newline at the end"}
	for _, c := range chunks {
		if n, err := b.Write([]byte(c)); err != nil || n != len(c) {
			t.Fatal(n, err)
		}
	}
	// Another writer (a GitHub note, say) appends while the build runs.
	b2, _ := r.openBuildLog(d)
	fmt.Fprintf(b2, "==> note: could not set the commit status on GitHub: 502\n")
	b2.Close()
	b.Close()

	raw, _ := os.ReadFile(r.buildLogPath(d))
	if want := strings.Join(chunks, "") + "==> note: could not set the commit status on GitHub: 502\n"; string(raw) != want {
		t.Fatalf("build.log changed:\n%q\nwant\n%q", raw, want)
	}
	var logs []string
	for _, l := range shipLines(t, r, d) {
		if l.Env != "prod" {
			t.Fatalf("labels %+v", l)
		}
		logs = append(logs, l.Log)
	}
	want := []string{"==> deploy dep_01 of shop/web", "#8 0.1 npm warn deprecated x", "#9 done", "==> note: could not set the commit status on GitHub: 502", "no newline at the end"}
	if !reflect.DeepEqual(logs, want) {
		t.Fatalf("shipped %q\nwant %q", logs, want)
	}
	if got := shipLines(t, r, d)[0].Time; got != "2026-10-06T10:00:01Z" {
		t.Fatalf("time %s", got)
	}

	// A preview says so.
	pd := &Deploy{ID: "dep_02", Project: "shop", App: "web", Preview: "7"}
	os.MkdirAll(r.workDir(pd), 0o755)
	pb, _ := r.openBuildLog(pd)
	fmt.Fprintln(pb, "==> FAILED: build: exit status 1")
	pb.Close()
	if l := shipLines(t, r, pd); len(l) != 1 || l[0].Env != "pr-7" {
		t.Fatalf("preview %+v", l)
	}

	// Without a log folder only build.log is written.
	r0 := &rt{opt: Options{DataDir: filepath.Join(dir, "runtime")}}
	d0 := &Deploy{ID: "dep_03", Project: "shop", App: "web"}
	os.MkdirAll(r0.workDir(d0), 0o755)
	b0, err := r0.openBuildLog(d0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(b0, "hello")
	b0.Close()
	if raw, _ := os.ReadFile(r0.buildLogPath(d0)); string(raw) != "hello\n" {
		t.Fatalf("%q", raw)
	}
}

func TestPruneBuildLogs(t *testing.T) {
	dir := t.TempDir()
	r := &rt{opt: Options{DataDir: filepath.Join(dir, "runtime"), LogDir: filepath.Join(dir, "logs")}}
	var ds []*Deploy // newest first
	for i := 14; i >= 1; i-- {
		d := &Deploy{ID: fmt.Sprintf("dep_%02d", i), Project: "shop", App: "web", Status: StatusSuperseded}
		if i == 2 {
			d.Status = StatusLive // an old live deploy keeps its logs
		}
		os.MkdirAll(r.workDir(d), 0o755)
		b, _ := r.openBuildLog(d)
		fmt.Fprintln(b, "x")
		b.Close()
		ds = append(ds, d)
	}
	// A preview's build in the same folder is not production's to prune.
	pd := &Deploy{ID: "dep_99", Project: "shop", App: "web", Preview: "pr-1"}
	os.MkdirAll(r.workDir(pd), 0o755)
	b, _ := r.openBuildLog(pd)
	fmt.Fprintln(b, "x")
	b.Close()

	r.pruneLogs("shop", "web", "", ds)
	ents, _ := os.ReadDir(r.buildShipDir("shop", "web"))
	var left []string
	for _, e := range ents {
		left = append(left, strings.TrimSuffix(e.Name(), ".log"))
	}
	want := []string{"dep_02", "dep_05", "dep_06", "dep_07", "dep_08", "dep_09", "dep_10", "dep_11", "dep_12", "dep_13", "dep_14", "dep_99"}
	if !reflect.DeepEqual(left, want) {
		t.Fatalf("left %v\nwant %v", left, want)
	}
	// build.log is the deploy record's, not pruned here.
	if _, err := os.Stat(r.buildLogPath(ds[13])); err != nil {
		t.Fatal(err)
	}
}
