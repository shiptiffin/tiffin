package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/runtime/srcpack"
)

// Deploying from a git URL: the box shallow-clones one commit of a public
// https repository and builds it like an upload. The clone is fenced in:
// https only (no file://, ssh or git:// and no submodules), no credentials,
// the host must resolve to public addresses only and git is pinned to the
// addresses that were checked (so DNS cannot be switched to an internal
// address between the check and the clone), no redirects, and size and
// time limits.

const (
	maxRepoURL   = 2048
	maxCloneSize = 512 << 20 // .git plus checkout
	cloneTimeout = 3 * time.Minute
	// maxProgressLine is the longest line of git's progress kept for the
	// build log; the rest of a longer one is dropped.
	maxProgressLine = 4 << 10
)

var (
	refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	// gitAllowPrivate lets tests clone from a loopback server.
	gitAllowPrivate = false
	// gitExtraConfig is extra `git -c` settings (tests trust their own CA).
	gitExtraConfig []string
	// cloneLimit is maxCloneSize (tests lower it).
	cloneLimit int64 = maxCloneSize
)

// gitSource is a validated repository to deploy from.
type gitSource struct {
	URL  *url.URL
	Ref  string // "" means the default branch (HEAD)
	Path string // subdirectory of the repository, "" for the top
	IPs  []netip.Addr
	// Auth is an Authorization header value for the clone (a GitHub
	// installation token). It reaches git through its environment, never
	// a file or the command line.
	Auth string
}

// resolver looks up a host's addresses (net.DefaultResolver in production).
type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func defaultResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// gitURLError is a request problem: a bad URL, ref or path (422).
type gitURLError struct{ msg, hint string }

func (e *gitURLError) Error() string { return e.msg }

const publicRepoHint = "Use the https URL of a public repository, e.g. https://github.com/owner/repo. " +
	"Private repositories: push to the box instead (tiffin git-remote --add, then git push tiffin main)."

// checkGitSource validates url, ref and path and resolves the host.
func checkGitSource(ctx context.Context, rawURL, ref, sub string, resolve resolver) (*gitSource, error) {
	bad := func(msg string) error { return &gitURLError{msg, publicRepoHint} }
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || len(rawURL) > maxRepoURL {
		return nil, bad("url is required (at most 2048 characters)")
	}
	if !strings.Contains(rawURL, "://") {
		return nil, bad("only https:// repository URLs are supported: for git@host:owner/repo use https://host/owner/repo")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, bad("url is not a valid URL: " + err.Error())
	}
	if u.Scheme != "https" {
		return nil, bad("only https:// repository URLs are supported (got " + orDefaultStr(u.Scheme, "no scheme") + ")")
	}
	if u.User != nil {
		return nil, bad("the URL must not carry credentials: only public repositories can be deployed from a URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, bad("the URL must not have a query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.Trim(u.Path, "/") == "" {
		return nil, bad("the URL needs a host and a repository path, e.g. https://github.com/owner/repo")
	}
	if p := u.Port(); p != "" && p != "443" && !gitAllowPrivate {
		return nil, bad("only the standard https port (443) is allowed")
	}
	if strings.HasPrefix(u.Path, "/-") || strings.Contains(u.Path, "/../") {
		return nil, bad("the repository path is not valid")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return nil, bad("the host " + host + " is not public")
	}
	if ref != "" && (!refRe.MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, ".lock") || strings.HasSuffix(ref, "/")) {
		return nil, &gitURLError{"ref must be a branch, tag or commit SHA: letters, digits, '.', '_', '-' and '/', not starting with '-'", "Leave ref empty for the repository's default branch."}
	}
	if sub != "" {
		clean := path.Clean(strings.ReplaceAll(sub, `\`, "/"))
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, &gitURLError{"path must be a directory inside the repository", "e.g. apps/web"}
		}
		if clean == "." {
			clean = ""
		}
		sub = clean
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err = resolve(rctx, host)
		cancel()
		if err != nil || len(ips) == 0 {
			return nil, &gitURLError{"cannot resolve " + host, "Check the URL; the box needs DNS and outbound HTTPS to fetch it."}
		}
	}
	for _, ip := range ips {
		if !publicIP(ip) && !gitAllowPrivate {
			if ip.String() == host {
				return nil, bad(host + " is not a public address")
			}
			return nil, bad(fmt.Sprintf("%s resolves to %s, which is not a public address", host, ip))
		}
	}
	// Lowercased, an explicit :443 dropped.
	switch port := u.Port(); {
	case port != "" && port != "443":
		u.Host = net.JoinHostPort(host, port) // tests only
	case strings.Contains(host, ":"):
		u.Host = "[" + host + "]"
	default:
		u.Host = host
	}
	return &gitSource{URL: u, Ref: ref, Path: sub, IPs: ips}, nil
}

func orDefaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// nonPublic are ranges that are not reachable public internet addresses.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private space
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 embeds any IPv4 address
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// String is the URL as cloned (for logs and the deploy record).
func (g *gitSource) String() string { return g.URL.String() }

// fetchGit clones one commit of src into dir (which must not exist) and
// returns the commit SHA. Progress goes to log.
func fetchGit(ctx context.Context, src *gitSource, dir string, log io.Writer) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	home, err := os.MkdirTemp("", "tiffin-git-home-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(home)
	pins := make([]string, len(src.IPs))
	for i, ip := range src.IPs {
		s := ip.Unmap().String()
		if ip.Is6() && !ip.Is4In6() {
			s = "[" + s + "]"
		}
		pins[i] = s
	}
	cfg := []string{
		"protocol.allow=never", "protocol.https.allow=always", // https only: no file://, ext::, ssh
		"http.followRedirects=false",
		"http.curloptResolve=" + src.URL.Hostname() + ":" + orDefaultStr(src.URL.Port(), "443") + ":" + strings.Join(pins, ","),
		"credential.helper=", "core.askPass=",
		"core.hooksPath=/dev/null", "core.fsmonitor=false",
		"submodule.recurse=false", "fetch.recurseSubmodules=false",
		"http.lowSpeedLimit=1024", "http.lowSpeedTime=30",
		"init.defaultBranch=main", "advice.detachedHead=false",
	}
	cfg = append(cfg, gitExtraConfig...)
	env := append(os.Environ(),
		"HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_PROTOCOL_FROM_USER=0",
		"GIT_LFS_SKIP_SMUDGE=1", "GIT_ALLOW_PROTOCOL=https")
	if src.Auth != "" {
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: "+src.Auth)
	}
	git := func(args ...string) *exec.Cmd {
		full := []string{}
		for _, c := range cfg {
			full = append(full, "-c", c)
		}
		c := exec.CommandContext(ctx, "git", append(full, args...)...)
		c.Env = env
		c.WaitDelay = 5 * time.Second
		return c
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if out, err := git("init", "-q", dir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git init: %v: %s", err, strings.TrimSpace(string(out)))
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	fmt.Fprintf(log, "==> cloning %s (%s, depth 1)\n", src, describeRef(src.Ref))
	began := time.Now()
	fetch := git("-C", dir, "fetch", "--depth", "1", "--no-tags", "--no-recurse-submodules", "--progress", "--", src.String(), ref)
	var out tailBuffer // the server's messages are its own: only the end is kept
	fetch.Stdout = io.MultiWriter(log, &out)
	fetch.Stderr = io.MultiWriter(&lineWriter{w: log}, &out)
	if err := fetch.Start(); err != nil {
		return "", err
	}
	// Watch the size while git downloads; stop a clone that grows too big.
	done := make(chan error, 1)
	go func() { done <- fetch.Wait() }()
	tooBig := false
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case err = <-done:
			break wait
		case <-tick.C:
			if dirSize(dir) > cloneLimit {
				tooBig = true
				_ = fetch.Process.Kill()
			}
		}
	}
	if tooBig {
		return "", &BuildError{Msg: fmt.Sprintf("the repository is larger than %s", humanBytes(cloneLimit)), Hint: "Deploy a smaller repository, or push just the app with tiffin deploy."}
	}
	if err != nil {
		return "", cloneError(ctx, out.String(), err)
	}
	// A small download can check out far bigger (one blob at many paths):
	// the commit's tree is measured before any of it is written.
	files, size, err := treeSize(git("-C", dir, "ls-tree", "-r", "-l", "-z", "FETCH_HEAD"))
	switch {
	case err != nil:
		return "", &BuildError{Msg: "could not read the commit's files: " + err.Error(), Hint: "The commit could not be checked out on this box."}
	case files > srcpack.DefaultLimits.MaxFiles:
		return "", &BuildError{Msg: fmt.Sprintf("the commit has more than %d files", srcpack.DefaultLimits.MaxFiles), Hint: "Deploy a smaller repository."}
	case size+dirSize(dir) > cloneLimit:
		return "", &BuildError{Msg: fmt.Sprintf("the checkout would be larger than %s", humanBytes(cloneLimit)), Hint: "Deploy a smaller repository."}
	}
	co := git("-C", dir, "checkout", "-q", "--detach", "FETCH_HEAD")
	var coOut tailBuffer
	co.Stdout, co.Stderr = &coOut, &coOut
	if err := co.Run(); err != nil {
		return "", &BuildError{Msg: "git checkout failed: " + strings.TrimSpace(lastLines(coOut.String(), 3)), Hint: "The commit could not be checked out on this box."}
	}
	if dirSize(dir) > cloneLimit {
		return "", &BuildError{Msg: fmt.Sprintf("the checkout is larger than %s", humanBytes(cloneLimit)), Hint: "Deploy a smaller repository."}
	}
	sha, err := git("-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	commit := strings.TrimSpace(string(sha))
	fmt.Fprintf(log, "==> cloned commit %s in %.1fs (%s)\n", commit[:min(12, len(commit))], time.Since(began).Seconds(), humanBytes(dirSize(dir)))
	return commit, nil
}

// treeSize runs ls (git ls-tree -r -l -z) and adds up the files of the
// tree it lists and their sizes, reading entry by entry. Submodules (no
// size) count as files of none.
func treeSize(ls *exec.Cmd) (files int, size int64, err error) {
	var errOut tailBuffer
	ls.Stderr = &errOut
	pipe, err := ls.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := ls.Start(); err != nil {
		return 0, 0, err
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 4<<10), 64<<10)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for sc.Scan() {
		// <mode> SP <type> SP <object> SP+ <size> TAB <path>
		meta, _, _ := bytes.Cut(sc.Bytes(), []byte{'\t'})
		f := strings.Fields(string(meta))
		files++
		if len(f) == 4 {
			if n, err := strconv.ParseInt(f[3], 10, 64); err == nil {
				size += n
			}
		}
		if files > srcpack.DefaultLimits.MaxFiles || size > cloneLimit {
			break // enough to refuse it
		}
	}
	scanErr := sc.Err()
	_ = ls.Process.Kill()
	werr := ls.Wait()
	if scanErr != nil {
		return files, size, scanErr
	}
	if files <= srcpack.DefaultLimits.MaxFiles && size <= cloneLimit && werr != nil {
		return files, size, fmt.Errorf("git ls-tree: %v: %s", werr, strings.TrimSpace(lastLines(errOut.String(), 3)))
	}
	return files, size, nil
}

func describeRef(ref string) string {
	if ref == "" {
		return "default branch"
	}
	return "ref " + ref
}

// httpDenied is an HTTP 401 or 403 in git's output (not digits of a port).
var httpDenied = regexp.MustCompile(`\b40[13]\b`)

// cloneError turns git's output into a deploy failure with a useful hint.
func cloneError(ctx context.Context, out string, err error) error {
	msg := strings.TrimSpace(lastLines(out, 3))
	low := strings.ToLower(out)
	switch {
	case ctx.Err() != nil:
		return &BuildError{Msg: fmt.Sprintf("the clone did not finish within %s", cloneTimeout), Hint: "The repository may be very large or the host slow. Try again, or push with tiffin deploy."}
	case strings.Contains(low, "could not read username") || strings.Contains(low, "authentication") || httpDenied.MatchString(low):
		return &BuildError{Msg: "the repository needs credentials: " + msg, Hint: publicRepoHint}
	case strings.Contains(low, "not found") || strings.Contains(low, "404"):
		return &BuildError{Msg: "repository not found: " + msg, Hint: "Check the URL (it must be public)."}
	case strings.Contains(low, "couldn't find remote ref") || strings.Contains(low, "not our ref"):
		return &BuildError{Msg: "no such branch, tag or commit: " + msg, Hint: "Check ref, or leave it empty for the default branch."}
	case strings.Contains(low, "redirect"):
		return &BuildError{Msg: "the server redirected the clone, which is not followed: " + msg, Hint: "Use the repository's canonical https URL."}
	}
	return &BuildError{Msg: "git fetch failed: " + orDefaultStr(msg, err.Error()), Hint: "Check the URL and that the repository is public."}
}

// lineWriter turns git's carriage-return progress into lines, keeping only
// the final state of each line so build logs stay readable.
type lineWriter struct {
	w   io.Writer
	buf []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		switch c {
		case '\r':
			l.buf = l.buf[:0]
		case '\n':
			if _, err := l.w.Write(append(bytes.TrimRight(l.buf, " "), '\n')); err != nil {
				return 0, err
			}
			l.buf = l.buf[:0]
		default:
			if len(l.buf) < maxProgressLine {
				l.buf = append(l.buf, c)
			}
		}
	}
	return len(p), nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// errNotDir is returned when path is not a directory of the clone.
var errNotDir = errors.New("not a directory")

// subdir returns dir/sub, making sure it is a directory inside dir (no
// symlink escapes).
func subdir(dir, sub string) (string, error) {
	if sub == "" {
		return dir, nil
	}
	p := filepath.Join(dir, filepath.FromSlash(sub))
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", errNotDir
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errNotDir
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", errNotDir
	}
	return real, nil
}
