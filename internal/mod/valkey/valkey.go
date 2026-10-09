// Package valkey is the Tiffin valkey module: one Valkey server on the data
// disk (AOF + RDB), and per project an ACL user that can only touch keys and
// pub/sub channels under its own prefix "p_<project>:". Apps get REDIS_URL
// (for Bun.redis or any Redis client) and VALKEY_PREFIX, plus an
// Upstash-compatible REST endpoint (rest.go) for @upstash/redis and @vercel/kv.
//
// Memory caps: Valkey has one maxmemory for the whole server and no
// per-prefix limit. The server-wide maxmemory (about an eighth of RAM) with
// volatile-lru eviction protects the box: keys with a TTL (caches) are
// evicted first, keys without one are never evicted (writes fail instead).
// A project with a limit is held to its own cache limit by measuring its
// prefix (limits.go); for the others maxMemoryMB is tracked, not enforced.
package valkey

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// Pinned upstream build (download.valkey.io, Ubuntu 24.04 "noble" binaries).
const (
	Version   = "9.1.2"
	sumArm64  = "eb601c28401d06d68d3fcb526645c4a01078c12d89de5fc70f5674c8a27bd2fd"
	sumX86_64 = "0d2a79936cbafa5f527a5175536bb17bd3f767f95f53529e59dddfacf762eec0"
)

// Layout on the box.
const (
	InstallDir = "/opt/tiffin/valkey/" + Version
	ConfDir    = "/etc/tiffin/valkey"
	DataDir    = "/var/lib/tiffin/valkey"
	ACLFile    = DataDir + "/users.acl"
	SocketDir  = "/var/run/valkey"
	SocketPath = SocketDir + "/valkey.sock"
	Port       = 6379
	UnitName   = "tiffin-valkey.service"
	adminUser  = "tiffin"
	adminPass  = ConfDir + "/admin.pass"
)

const (
	nsPassword     = "valkey.password"      // project → ACL user password (age-encrypted)
	nsReadPassword = "valkey.read-password" // project → read-only ACL user password (age-encrypted)
	nsCap          = "valkey.cap"           // project → maxMemoryMB
)

func init() { platform.Register(&Module{}) }

// Module implements the valkey module.
type Module struct{}

func (*Module) Name() string { return "valkey" }
func (*Module) Order() int   { return 10 }

// Prefix is the key and channel prefix of a project: "p_my_shop:".
func Prefix(project string) string { return datakit.Ident(project) + ":" }

// User is the project's ACL user.
func User(project string) string { return datakit.Ident(project) }

// Admin connects to the server as the box's admin user over the socket.
func Admin(ctx context.Context) (*Client, error) {
	pw, err := os.ReadFile(adminPass)
	if err != nil {
		return nil, fmt.Errorf("valkey admin password: %w", err)
	}
	return Dial(ctx, "unix", SocketPath, adminUser, strings.TrimSpace(string(pw)))
}

// Provision installs the pinned Valkey build and runs it as a systemd unit.
func (*Module) Provision(ctx context.Context, s *platform.System) error {
	arch, sum := "arm64", sumArm64
	if runtime.GOARCH == "amd64" {
		arch, sum = "x86_64", sumX86_64
	}
	if _, err := os.Stat(InstallDir + "/bin/valkey-server"); err != nil {
		tgz, err := s.Fetch(ctx, fmt.Sprintf("https://download.valkey.io/releases/valkey-%s-noble-%s.tar.gz", Version, arch), sum)
		if err != nil {
			return err
		}
		if err := s.Untar(ctx, tgz, InstallDir, 1); err != nil {
			return err
		}
	}
	if err := s.User(ctx, "valkey", DataDir); err != nil {
		return err
	}
	if _, err := s.Sh(ctx, `install -d -o valkey -g valkey -m 0750 `+DataDir+`
install -d -o root -g valkey -m 0750 `+ConfDir+`
ln -sfn `+InstallDir+`/bin/valkey-cli /usr/local/bin/valkey-cli`); err != nil {
		return err
	}
	pw, err := os.ReadFile(adminPass)
	if err != nil {
		pw = []byte(datakit.Password())
		if err := os.WriteFile(adminPass, pw, 0o600); err != nil {
			return err
		}
	}
	aclChanged, err := ensureACLFile(ACLFile, strings.TrimSpace(string(pw)))
	if err != nil {
		return err
	}
	if aclChanged {
		if _, err := s.Run(ctx, "chown", "valkey:valkey", ACLFile); err != nil {
			return err
		}
	}
	conf := Config(memTotalMB())
	if _, err := s.WriteFile(ConfDir+"/valkey.conf", []byte(conf), 0o640); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "chown", "root:valkey", ConfDir+"/valkey.conf"); err != nil {
		return err
	}
	h := sha256.Sum256([]byte(conf))
	if err := s.Unit(ctx, UnitName, unit(hex.EncodeToString(h[:])[:16])); err != nil {
		return err
	}
	if aclChanged {
		if _, err := s.Run(ctx, "systemctl", "restart", UnitName); err != nil {
			return err
		}
	}
	return waitReady(ctx, 60*time.Second)
}

func waitReady(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		c, err := Admin(ctx)
		if err == nil {
			_, err = c.String(ctx, "PING")
			c.Close()
			if err == nil {
				return nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("valkey did not become ready in %s: %v", d, last)
}

// WaitReady waits until the server answers the admin user.
func WaitReady(ctx context.Context, d time.Duration) error { return waitReady(ctx, d) }

// ensureACLFile makes sure the ACL file at path disables the default user
// and has the box's admin user, keeping every project user. It reports a change.
func ensureACLFile(path, pw string) (bool, error) {
	sum := sha256.Sum256([]byte(pw))
	hash := "#" + hex.EncodeToString(sum[:])
	cur, _ := os.ReadFile(path)
	var keep []string
	okDefault, okAdmin := false, false
	sc := bufio.NewScanner(strings.NewReader(string(cur)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "user default "):
			okDefault = strings.Contains(" "+line+" ", " off ")
		case strings.HasPrefix(line, "user "+adminUser+" "):
			okAdmin = strings.Contains(line, hash) && strings.Contains(" "+line+" ", " on ")
		default:
			keep = append(keep, line)
		}
	}
	if okDefault && okAdmin {
		return false, nil
	}
	lines := append([]string{
		"user default off resetkeys resetchannels -@all",
		"user " + adminUser + " on " + hash + " ~* &* +@all",
	}, keep...)
	if err := os.WriteFile(path+".tmp", []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(path+".tmp", path)
}

func memTotalMB() int {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 2048
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
			if kb > 0 {
				return kb / 1024
			}
		}
	}
	return 2048
}

// MaxMemoryMB is the server-wide memory limit for a box with memMB of RAM.
func MaxMemoryMB(memMB int) int { return max(128, min(memMB/8, 4096)) }

// Config renders valkey.conf.
func Config(memMB int) string {
	return fmt.Sprintf(`# Managed by Tiffin (tiffin provision). Edits are overwritten.
bind 127.0.0.1 -::1
port %[1]d
protected-mode yes
unixsocket %[2]s
unixsocketperm 777
daemonize no
supervised no
loglevel notice
logfile ""
databases 16

# Users live in the ACL file (default user off; one user per project).
aclfile %[3]s

# Durability: append-only file (fsync every second) with an RDB preamble,
# plus RDB snapshots the backup module copies.
dir %[4]s
appendonly yes
appendfsync everysec
aof-use-rdb-preamble yes
dbfilename dump.rdb
save 3600 1 300 100 60 10000

# Sized for a %[5]d MB box. Keys with a TTL are evicted first; keys
# without one are never evicted (writes fail with OOM instead).
maxmemory %[6]dmb
maxmemory-policy volatile-lru

# A Lua script that runs past this many milliseconds gets every other
# client BUSY replies; the box's script guard then kills it (or, when it
# already wrote and can't be killed, restarts the server from its AOF).
busy-reply-threshold %[7]d
`, Port, SocketPath, ACLFile, DataDir, memMB, MaxMemoryMB(memMB), scriptBusyMS)
}

func unit(confSum string) string {
	return `[Unit]
Description=Tiffin Valkey ` + Version + `
After=network.target local-fs.target
RequiresMountsFor=/var/lib/tiffin

[Service]
Type=simple
User=valkey
Group=valkey
Environment=TIFFIN_VALKEY_CONF=` + confSum + `
ExecStartPre=+/usr/bin/install -d -m 0755 -o valkey -g valkey ` + SocketDir + `
ExecStart=` + InstallDir + `/bin/valkey-server ` + ConfDir + `/valkey.conf
LimitNOFILE=65536
TimeoutStopSec=120
Restart=always
RestartSec=2
OOMScoreAdjust=-500

[Install]
WantedBy=multi-user.target
`
}

// Kinds implements platform.Reconciler.
func (*Module) Kinds() []string { return []string{"service/valkey", change.EmptyAddress("valkey")} }

// Reconcile creates, updates or removes a project's ACL user.
func (*Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	if address == change.EmptyAddress("valkey") {
		return reconcileEmptied(ctx, p, project, spec)
	}
	c, err := Admin(ctx)
	if err != nil {
		return fmt.Errorf("connect to valkey: %w", err)
	}
	defer c.Close()
	user, prefix := User(project), Prefix(project)
	if spec == nil {
		if _, err := c.Do(ctx, "ACL", "DELUSER", user, ReadUser(project)); err != nil {
			return err
		}
		_ = p.DB.KVDelete(ctx, nsReadPassword, project)
		if _, err := c.Do(ctx, "ACL", "SAVE"); err != nil {
			return err
		}
		if _, err := deletePrefix(ctx, c, prefix); err != nil {
			return err
		}
		limits.forget(project)
		_ = p.DB.KVDelete(ctx, nsCap, project)
		return p.DB.KVDelete(ctx, nsPassword, project)
	}
	var s manifest.Valkey
	if err := json.Unmarshal(spec, &s); err != nil {
		return fmt.Errorf("valkey spec: %w", err)
	}
	pw, err := datakit.EnsureSecret(ctx, p, nsPassword, project)
	if err != nil {
		return err
	}
	rules := ACLRules(project, pw)
	if limits.held(project) {
		rules = append(rules, holdRules...) // over its cache limit: keep refusing writes
	}
	if _, err := c.Do(ctx, append([]string{"ACL", "SETUSER", user}, rules...)...); err != nil {
		return fmt.Errorf("ACL SETUSER: %w", err)
	}
	if _, err := c.Do(ctx, "ACL", "SAVE"); err != nil {
		return err
	}
	return p.DB.KVPut(ctx, nsCap, project, []byte(strconv.Itoa(s.MaxMemoryMB)))
}

// ACLRules are the ACL SETUSER rules for a project user: its own key and
// channel prefix, every data command, nothing administrative, and no
// commands that reveal other projects' key names (KEYS, SCAN, RANDOMKEY)
// or switch databases.
func ACLRules(project, password string) []string {
	prefix := Prefix(project)
	return append([]string{"reset", "on", ">" + password, "~" + prefix + "*", "&" + prefix + "*"}, commandRules...)
}

// ReadUser is a project's read-only ACL user.
func ReadUser(project string) string { return User(project) + "__read" }

// ReadEnv is REDIS_URL, VALKEY_URL and VALKEY_PREFIX for a user that can
// read the project's keys and nothing more: app builds get it. The user is
// made (or updated) on each call.
func ReadEnv(ctx context.Context, p *platform.Platform, project string) (map[string]string, error) {
	pw, err := datakit.EnsureSecret(ctx, p, nsReadPassword, project)
	if err != nil {
		return nil, err
	}
	c, err := Admin(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to valkey: %w", err)
	}
	defer c.Close()
	prefix := Prefix(project)
	rules := []string{"reset", "on", ">" + pw, "%R~" + prefix + "*", "+@read", "+@connection", "-@dangerous", "-scan", "-randomkey", "-select"}
	if _, err := c.Do(ctx, append([]string{"ACL", "SETUSER", ReadUser(project)}, rules...)...); err != nil {
		return nil, fmt.Errorf("ACL SETUSER: %w", err)
	}
	if _, err := c.Do(ctx, "ACL", "SAVE"); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "redis", User: url.UserPassword(ReadUser(project), pw), Host: "127.0.0.1:" + strconv.Itoa(Port)}
	env := map[string]string{"REDIS_URL": u.String(), "VALKEY_URL": u.String(), "VALKEY_PREFIX": prefix}
	// The REST tokens too: a build reads through either, never writes.
	rest, err := RESTEnv(ctx, p, project)
	if err != nil {
		return nil, err
	}
	ro := rest["KV_REST_API_READ_ONLY_TOKEN"]
	env["UPSTASH_REDIS_REST_TOKEN"], env["KV_REST_API_TOKEN"] = ro, ro
	return env, nil
}

// commandRules are the commands a project user may run. Lua scripts
// (EVAL, EVALSHA, SCRIPT LOAD) stay: rate limiters and queues need them,
// and the script guard (scripts.go) stops one that runs too long. Functions
// do not: a library is server-wide, so one project could replace another's.
// Killing or flushing scripts is the box's job, not a project's.
var commandRules = []string{"+@all", "-@admin", "-@dangerous", "+info", "-scan", "-randomkey", "-select", "-move", "-swapdb",
	"-function", "-fcall", "-fcall_ro", "-script|kill", "-script|flush"}

// deletePrefix removes every key under prefix and returns how many.
func deletePrefix(ctx context.Context, c *Client, prefix string) (int, error) {
	n := 0
	cursor := "0"
	for {
		v, err := c.Do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "1000")
		if err != nil {
			return n, err
		}
		next, keys := scanReply(v)
		cursor = next
		if len(keys) > 0 {
			if _, err := c.Do(ctx, append([]string{"UNLINK"}, keys...)...); err != nil {
				return n, err
			}
			n += len(keys)
		}
		if cursor == "0" {
			return n, nil
		}
	}
}

func scanReply(v any) (string, []string) {
	arr, _ := v.([]any)
	if len(arr) != 2 {
		return "0", nil
	}
	cursor, _ := arr[0].(string)
	items, _ := arr[1].([]any)
	keys := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			keys = append(keys, s)
		}
	}
	return cursor, keys
}

// HasService reports whether the project's manifest has a valkey service.
func HasService(ctx context.Context, p *platform.Platform, project string) (bool, error) {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return false, err
	}
	_, ok := res[change.KindService+"/valkey"]
	return ok, nil
}

// ConnEnv returns REDIS_URL, VALKEY_URL and VALKEY_PREFIX for a project.
// With viaSocket the URLs use the unix socket (redis+unix://), for
// containers that bind-mount /var/run/valkey instead of sharing the host network.
func ConnEnv(ctx context.Context, p *platform.Platform, project string, viaSocket bool) (map[string]string, error) {
	pw, err := datakit.EnsureSecret(ctx, p, nsPassword, project)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "redis", User: url.UserPassword(User(project), pw), Host: "127.0.0.1:" + strconv.Itoa(Port)}
	if viaSocket {
		u = url.URL{Scheme: "redis+unix", User: url.UserPassword(User(project), pw), Path: SocketPath}
	}
	return map[string]string{"REDIS_URL": u.String(), "VALKEY_URL": u.String(), "VALKEY_PREFIX": Prefix(project)}, nil
}

// Env implements platform.EnvProvider.
func (*Module) Env(ctx context.Context, p *platform.Platform, project, app string) (map[string]string, error) {
	ok, err := HasService(ctx, p, project)
	if err != nil || !ok {
		return nil, err
	}
	env, err := ConnEnv(ctx, p, project, false)
	if err != nil {
		return nil, err
	}
	rest, err := RESTEnv(ctx, p, project)
	for k, v := range rest {
		env[k] = v
	}
	return env, err
}

// Checks reports whether Valkey answers and how full it is.
func (*Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := Admin(ctx)
	if err != nil {
		return []platform.Check{{Name: "valkey", OK: false, Detail: "not answering on " + SocketPath + ": " + err.Error()}}
	}
	defer c.Close()
	raw, err := c.String(ctx, "INFO", "memory")
	if err != nil {
		return []platform.Check{{Name: "valkey", OK: false, Detail: err.Error()}}
	}
	info := parseInfo(raw)
	used, _ := strconv.ParseInt(info["used_memory"], 10, 64)
	limit, _ := strconv.ParseInt(info["maxmemory"], 10, 64)
	detail := fmt.Sprintf("Valkey %s up, %s used of %s", Version, mb(used), mb(limit))
	ok := limit == 0 || used < limit*95/100
	if !ok {
		detail += " (nearly full: writes of keys without a TTL will fail)"
	}
	return []platform.Check{{Name: "valkey", OK: ok, Detail: detail}}
}

func mb(b int64) string { return fmt.Sprintf("%.1f MB", float64(b)/(1<<20)) }
