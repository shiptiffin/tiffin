package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// Host tools (railpack, buildctl, nerdctl) run as root on the box. Their
// environment is the box's own (toolEnv) and nothing else: an app's env
// travels as arguments, or under names the box picks (secretArgs), never
// under the app's own names. Otherwise a variable such as DOCKER_CONFIG,
// PATH, NERDCTL_TOML or LD_PRELOAD would reconfigure a root process.

// secretArgs are buildctl's --secret flags for keys: each secret keeps the
// app's name as its id (what a build mounts) while its value travels in
// buildctl's environment under a name of the box's own (TIFFIN_SECRET_<n>),
// returned in env.
func secretArgs(keys []string, values map[string]string) (args, env []string) {
	for i, k := range keys {
		t := "TIFFIN_SECRET_" + strconv.Itoa(i)
		args = append(args, "--secret", "id="+k+",env="+t)
		env = append(env, t+"="+values[k])
	}
	return args, env
}

// cacheNamespace is the prefix of an app's BuildKit cache mounts: a hash of
// the project and app, fixed-length so no other pair (nor a cache name a
// repository picks) can produce another app's prefix.
func cacheNamespace(project, app string) string {
	raw, _ := json.Marshal([]string{project, app})
	h := sha256.Sum256(raw)
	return "tiffin-" + hex.EncodeToString(h[:16])
}

// maxBuildLine is the longest line buildOutput looks at; the rest of a
// longer line is skipped (the build log still has all of it).
const maxBuildLine = 64 << 10

// buildOutput watches buildctl's output on its way to the build log and
// keeps only what the box reads from it: the first line that names a
// cause, whether the build ran out of memory, the exported image's digest,
// and whether any of marks appeared. Its memory stays bounded however much
// a build prints.
type buildOutput struct {
	marks []string

	mu     sync.Mutex
	line   []byte
	first  string
	oom    bool
	digest string
	seen   map[string]bool
}

func (o *buildOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		room := maxBuildLine - len(o.line)
		if len(chunk) > room {
			chunk = chunk[:max(room, 0)]
		}
		o.line = append(o.line, chunk...)
		if i < 0 {
			break
		}
		o.endLine()
		p = p[i+1:]
	}
	return n, nil
}

// Close looks at a last line without a newline.
func (o *buildOutput) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.line) > 0 {
		o.endLine()
	}
	return nil
}

func (o *buildOutput) endLine() {
	line := string(o.line)
	o.line = o.line[:0]
	if o.first == "" {
		o.first = errorLine(line)
	}
	if strings.Contains(line, "exit code: 137") || strings.Contains(line, "Killed") {
		o.oom = true
	}
	if m := manifestDigest.FindStringSubmatch(line); m != nil {
		o.digest = m[1]
	}
	for _, m := range o.marks {
		if strings.Contains(line, m) {
			if o.seen == nil {
				o.seen = map[string]bool{}
			}
			o.seen[m] = true
		}
	}
}

// result is what the output said; call after the command ended.
func (o *buildOutput) result() (first string, oom bool, digest string) {
	_ = o.Close()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.first, o.oom, o.digest
}

// saw reports whether a line held mark (one of marks).
func (o *buildOutput) saw(mark string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.seen[mark]
}
