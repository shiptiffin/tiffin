// Package storage is the Tiffin storage module: S3-compatible buckets on the
// box's data disk.
//
// How it fits together:
//
//   - versitygw (Apache-2.0), a pinned and checksum-verified release binary,
//     runs under systemd as tiffin-storage.service with the posix backend on
//     /var/lib/tiffin/storage/data. It listens on 127.0.0.1:7480 only and keeps
//     its accounts in its internal IAM (/var/lib/tiffin/storage/iam).
//   - Each project gets its own versitygw account (role "user"), so its key
//     can only reach the buckets it owns. The key pair is kept encrypted in the
//     box's secret store.
//   - Bucket "media" of project "shop" is the S3 bucket "shop-media". Public
//     buckets carry an anonymous-read bucket policy.
//   - A small front server inside tiffin (127.0.0.1:7481) sits before the
//     gateway: it enforces storage limits and read-only holds on uploads, proxies every other
//     S3 call untouched (Host preserved, so signatures and presigned URLs
//     verify), and serves public files at files.<domain>/<project>/<bucket>/<key>.
//   - The edge routes s3.<domain> and files.<domain> to the front server.
//   - Deleting a bucket moves its directory into /var/lib/tiffin/trash/storage
//     for 7 days; re-creating it (which is what undo does) brings it back.
package storage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/backup"
	"github.com/btahir/tiffin/internal/platform"
)

// mod is the registered module (tests make their own).
var mod = &Module{}

func init() {
	platform.Register(mod)
	// Every backup set copies the whole storage tree (objects, accounts, root
	// key, audit manifests) with reflinks; the "files" restore target puts
	// it back.
	backup.Include("storage", storageDir(boxRoot))
}

// Pinned versitygw release.
const (
	GatewayVersion = "v1.8.0"
	releaseBase    = "https://github.com/versity/versitygw/releases/download/" + GatewayVersion + "/versitygw_" + GatewayVersion + "_Linux_"
)

// releaseSHA256 pins the release tarball per GOARCH (from the release's checksums).
var releaseSHA256 = map[string]struct{ asset, sum string }{
	"arm64": {"arm64.tar.gz", "b34051d33f5a9c457f790896acb7bd7d7e15ad8d92efb70616b924f37e401910"},
	"amd64": {"x86_64.tar.gz", "2ba2c734d10d2c4e651d03182cb4b246656bc735a2f282db7b0b73fba6073467"},
}

const (
	// GatewayAddr is where versitygw listens (loopback only).
	GatewayAddr = "127.0.0.1:7480"
	// FrontPort is the front server's port: S3 with quotas, and public files.
	FrontPort = 7481
	// Region is the S3 region every client should use.
	Region = "us-east-1"

	healthPath  = "/health"
	unitName    = "tiffin-storage.service"
	serviceUser = "tiffin-storage"
	installDir  = "/usr/local/lib/tiffin/versitygw-" + GatewayVersion
	binPath     = installDir + "/versitygw"
	boxRoot     = "/var/lib/tiffin"

	// TrashRetention is how long deleted buckets stay restorable.
	TrashRetention = 7 * 24 * time.Hour
	// DefaultQuotaBytes is the per-project storage limit (database and files)
	// until the owner sets one: none. The box's disk guard keeps one project
	// from filling the disk; a limit is for sharing it more tightly.
	DefaultQuotaBytes int64 = 0

	kvNS = "storage"
	// Secrets for every project's S3 account live under this pseudo-project
	// (it can never be a real project: slugs start with a letter).
	secretsProject = "_storage"
)

// Dirs under the data root.
func storageDir(root string) string { return filepath.Join(root, "storage") }
func dataDir(root string) string    { return filepath.Join(root, "storage", "data") }
func iamDir(root string) string     { return filepath.Join(root, "storage", "iam") }
func rootEnv(root string) string    { return filepath.Join(root, "storage", "root.env") }
func auditDir(root string) string   { return filepath.Join(root, "storage", "audit") }
func trashDir(root string) string   { return filepath.Join(root, "trash", "storage") }

// Module implements the storage module.
type Module struct {
	mu    sync.Mutex
	gw    *gateway
	usage *usageTracker
	front *frontServer

	// Tests override these; empty means the box defaults.
	bin       string
	gwAddr    string
	frontAddr string
	extraIP   func(ctx context.Context, p *platform.Platform) string
}

func (*Module) Name() string { return "storage" }
func (*Module) Order() int   { return 20 }

// ---- provisioning ----

// Provision installs the pinned versitygw, its system user, directories,
// root credentials and the systemd unit.
func (*Module) Provision(ctx context.Context, s *platform.System) error {
	rel, ok := releaseSHA256[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("storage: no versitygw build pinned for %s", runtime.GOARCH)
	}
	if _, err := os.Stat(binPath); err != nil {
		tgz, err := s.Fetch(ctx, releaseBase+rel.asset, rel.sum)
		if err != nil {
			return err
		}
		if err := s.Untar(ctx, tgz, installDir, 1); err != nil {
			return err
		}
	}
	if err := s.User(ctx, serviceUser, storageDir(boxRoot)); err != nil {
		return err
	}
	u, err := user.Lookup(serviceUser)
	if err != nil {
		return fmt.Errorf("storage: look up %s: %w", serviceUser, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := fixLayout(boxRoot, 0, 0, uid, gid); err != nil {
		return err
	}
	if _, err := os.Stat(rootEnv(boxRoot)); errors.Is(err, os.ErrNotExist) {
		c := newCreds("TFNROOT")
		env := "ROOT_ACCESS_KEY=" + c.AccessKey + "\nROOT_SECRET_KEY=" + c.Secret + "\n"
		if _, err := s.WriteFile(rootEnv(boxRoot), []byte(env), 0o600); err != nil {
			return err
		}
	}
	if err := os.Chown(rootEnv(boxRoot), 0, 0); err != nil {
		return err
	}
	if err := s.Unit(ctx, unitName, unitFile()); err != nil {
		return unitError(ctx, s, err)
	}
	if err := s.WaitTCP(ctx, GatewayAddr, 30*time.Second); err != nil {
		return unitError(ctx, s, err)
	}
	return nil
}

// unitError names the unit and carries its last journal lines.
func unitError(ctx context.Context, s *platform.System, err error) error {
	out, _ := s.Run(ctx, "journalctl", "-u", unitName, "-n", "20", "--no-pager", "-o", "cat")
	return fmt.Errorf("%s did not start: %w\nlast log lines (journalctl -u %s):\n%s", unitName, err, unitName, strings.TrimSpace(out))
}

// layoutDir is one directory of the storage layout with its owner and mode.
type layoutDir struct {
	path    string
	service bool // owned by the service user (else root)
	group   bool // group is the service user's (traversable by it)
	mode    os.FileMode
}

func layout(root string) []layoutDir {
	return []layoutDir{
		{storageDir(root), false, true, 0o750}, // root:tiffin-storage, the service user can traverse
		{dataDir(root), true, true, 0o750},     // the gateway's posix root
		{iamDir(root), true, true, 0o750},      // the gateway's accounts
		{auditDir(root), false, false, 0o700},  // tiffin only
		{filepath.Join(root, "trash"), false, false, 0o700},
		{trashDir(root), false, false, 0o700},
	}
}

// fixLayout creates the storage directories and resets their owners and
// modes on every provision, so a box with an older or hand-edited tree
// recovers. Bucket contents are left alone; IAM files go to the service user.
func fixLayout(root string, rootUID, rootGID, uid, gid int) error {
	for _, d := range layout(root) {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return err
		}
		ou, og := rootUID, rootGID
		if d.service {
			ou = uid
		}
		if d.group {
			og = gid
		}
		if err := os.Chown(d.path, ou, og); err != nil {
			return fmt.Errorf("storage: chown %s: %w", d.path, err)
		}
		if err := os.Chmod(d.path, d.mode); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(iamDir(root))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Lchown(filepath.Join(iamDir(root), e.Name()), uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func unitFile() string {
	return `[Unit]
Description=Tiffin storage (versitygw ` + GatewayVersion + `, S3 on the data disk)
After=network.target local-fs.target
RequiresMountsFor=/var/lib/tiffin

[Service]
User=` + serviceUser + `
Group=` + serviceUser + `
EnvironmentFile=` + rootEnv(boxRoot) + `
ExecStart=` + binPath + ` --port ` + GatewayAddr + ` --region ` + Region + ` --iam-dir ` + iamDir(boxRoot) + ` --health ` + healthPath + ` --quiet posix ` + dataDir(boxRoot) + `
Restart=always
RestartSec=2
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=` + dataDir(boxRoot) + ` ` + iamDir(boxRoot) + `

[Install]
WantedBy=multi-user.target
`
}

// newCreds makes an access key (prefix + 16 base32 chars) and a 40-char secret.
func newCreds(prefix string) Creds {
	var a [10]byte
	var s [30]byte
	_, _ = rand.Read(a[:])
	_, _ = rand.Read(s[:])
	return Creds{
		AccessKey: prefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(a[:])[:16],
		Secret:    base64.RawURLEncoding.EncodeToString(s[:])[:40],
	}
}

// readRootEnv parses root.env.
func readRootEnv(path string) (Creds, error) {
	f, err := os.Open(path)
	if err != nil {
		return Creds{}, err
	}
	defer f.Close()
	var c Creds
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch k {
		case "ROOT_ACCESS_KEY":
			c.AccessKey = v
		case "ROOT_SECRET_KEY":
			c.Secret = v
		}
	}
	if c.AccessKey == "" || c.Secret == "" {
		return c, fmt.Errorf("%s has no root credentials", path)
	}
	return c, sc.Err()
}

// ErrNotInstalled means the box has no storage gateway (not provisioned).
var ErrNotInstalled = errors.New("storage is not installed on this box: run `tiffin up` to provision it")

// gateway returns the gateway client, reading the root credentials once.
func (m *Module) gateway(p *platform.Platform) (*gateway, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gw != nil {
		return m.gw, nil
	}
	root, err := readRootEnv(rootEnv(p.DataRoot))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		return nil, err
	}
	bin, addr := m.bin, m.gwAddr
	if bin == "" {
		bin = binPath
	}
	if addr == "" {
		addr = GatewayAddr
	}
	m.gw = &gateway{bin: bin, addr: addr, root: root, region: Region, client: &http.Client{Timeout: 5 * time.Minute}}
	return m.gw, nil
}

func (m *Module) tracker() *usageTracker {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usage == nil {
		m.usage = newUsageTracker()
	}
	return m.usage
}

// ---- names, metadata, credentials ----

// S3Name is the gateway bucket for a project's bucket: "<project>-<bucket>".
func S3Name(project, bucket string) string { return project + "-" + bucket }

// bucketMeta is what the module records about a converged bucket.
type bucketMeta struct {
	Project   string    `json:"project"`
	Name      string    `json:"name"`
	Public    bool      `json:"public"`
	CreatedAt time.Time `json:"createdAt"`
}

func getMeta(ctx context.Context, p *platform.Platform, s3name string) (*bucketMeta, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "bucket/"+s3name)
	if err != nil || !ok {
		return nil, err
	}
	var b bucketMeta
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func putMeta(ctx context.Context, p *platform.Platform, s3name string, b *bucketMeta) error {
	raw, _ := json.Marshal(b)
	return p.DB.KVPut(ctx, kvNS, "bucket/"+s3name, raw)
}

// allMeta returns every bucket's metadata keyed by S3 name.
func allMeta(ctx context.Context, p *platform.Platform) (map[string]*bucketMeta, error) {
	kv, err := p.DB.KVList(ctx, kvNS)
	if err != nil {
		return nil, err
	}
	out := map[string]*bucketMeta{}
	for k, v := range kv {
		if name, ok := strings.CutPrefix(k, "bucket/"); ok {
			var b bucketMeta
			if json.Unmarshal(v, &b) == nil {
				out[name] = &b
			}
		}
	}
	return out, nil
}

func secretName(project string) string {
	return "KEY_" + strings.ToUpper(strings.ReplaceAll(project, "-", "_"))
}

// credsFor returns a project's S3 key pair, creating and storing it (encrypted)
// when create is set.
func credsFor(ctx context.Context, p *platform.Platform, project string, create bool) (Creds, bool, error) {
	if p.Secrets == nil {
		return Creds{}, false, errors.New("storage: the box secret store is not available")
	}
	all, err := p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return Creds{}, false, err
	}
	if raw, ok := all[secretName(project)]; ok {
		var c Creds
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return Creds{}, false, fmt.Errorf("storage credentials for %s are corrupt: %w", project, err)
		}
		return c, true, nil
	}
	if !create {
		return Creds{}, false, nil
	}
	c := newCreds("TFN")
	raw, _ := json.Marshal(c)
	if err := p.Secrets.Set(ctx, secretsProject, secretName(project), string(raw), "storage"); err != nil {
		return Creds{}, false, err
	}
	return c, true, nil
}

// projectStorage reports whether the project wants storage, and its buckets.
func projectStorage(ctx context.Context, p *platform.Platform, project string) (bool, map[string]manifest.Bucket, error) {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return false, nil, err
	}
	_, on := res[change.KindService+"/storage"]
	buckets := map[string]manifest.Bucket{}
	for addr, r := range res {
		if change.Kind(addr) == change.KindBucket {
			var b manifest.Bucket
			_ = json.Unmarshal(r.Spec, &b)
			buckets[change.Name(addr)] = b
		}
	}
	return on, buckets, nil
}

// ---- reconcile ----

func (*Module) Kinds() []string { return []string{change.KindService + "/storage", change.KindBucket} }

func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	gw, err := m.gateway(p)
	if err != nil {
		return err
	}
	if err := gw.waitHealthy(ctx, 30*time.Second); err != nil {
		return fmt.Errorf("the storage gateway is not answering (systemctl status %s): %w", unitName, err)
	}
	defer m.tracker().invalidate()
	if address == change.KindService+"/storage" {
		if spec == nil {
			c, ok, err := credsFor(ctx, p, project, false)
			if err != nil || !ok {
				return err
			}
			// Keep the stored key so re-enabling storage restores the same one.
			return gw.deleteUser(ctx, c.AccessKey)
		}
		c, _, err := credsFor(ctx, p, project, true)
		if err != nil {
			return err
		}
		return gw.ensureUser(ctx, c)
	}

	name := change.Name(address)
	s3name := S3Name(project, name)
	if spec == nil {
		return m.trashBucket(ctx, p, project, name)
	}
	if len(s3name) > 63 {
		return fmt.Errorf("bucket %q: the S3 name %q is %d characters; S3 allows 63. Use a shorter bucket name", name, s3name, len(s3name))
	}
	var b manifest.Bucket
	if err := json.Unmarshal(spec, &b); err != nil {
		return err
	}
	meta, err := getMeta(ctx, p, s3name)
	if err != nil {
		return err
	}
	if meta != nil && (meta.Project != project || meta.Name != name) {
		return fmt.Errorf("bucket %q: the S3 name %q already belongs to bucket %q of project %q; pick another bucket name", name, s3name, meta.Name, meta.Project)
	}
	c, _, err := credsFor(ctx, p, project, true)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dataDir(p.DataRoot), s3name)); errors.Is(err, os.ErrNotExist) {
		restored, err := m.restoreBucket(ctx, p, project, name)
		if err != nil {
			return err
		}
		if restored {
			p.Log.Info("storage: restored bucket from trash", "project", project, "bucket", name)
			if err := gw.changeOwner(ctx, s3name, c.AccessKey); err != nil {
				return err
			}
		} else if err := gw.createBucket(ctx, s3name, c.AccessKey); err != nil {
			return err
		}
	}
	if err := gw.setPublic(ctx, s3name, c.AccessKey, b.Public); err != nil {
		return err
	}
	created := time.Now().UTC()
	if meta != nil {
		created = meta.CreatedAt
	}
	return putMeta(ctx, p, s3name, &bucketMeta{Project: project, Name: name, Public: b.Public, CreatedAt: created})
}

// ---- env ----

// containerHost is the address apps reach box services on. The runtime
// module publishes it as KV runtime/host-ip; until then apps share the
// host's network and 127.0.0.1 works.
func (m *Module) containerHost(ctx context.Context, p *platform.Platform) string {
	if m.extraIP != nil {
		if ip := m.extraIP(ctx, p); ip != "" {
			return ip
		}
		return "127.0.0.1"
	}
	if raw, ok, _ := p.DB.KVGet(ctx, "runtime", "host-ip"); ok && len(bytes.TrimSpace(raw)) > 0 {
		return string(bytes.TrimSpace(raw))
	}
	return "127.0.0.1"
}

// envName turns a bucket name into an env suffix: "user-uploads" → "USER_UPLOADS".
func envName(bucket string) string { return strings.ToUpper(strings.ReplaceAll(bucket, "-", "_")) }

// Env gives a project's apps S3 settings that Bun.s3, the AWS SDKs and
// aws-cli read unmodified.
func (m *Module) Env(ctx context.Context, p *platform.Platform, project, _ string) (map[string]string, error) {
	on, buckets, err := projectStorage(ctx, p, project)
	if err != nil || !on {
		return nil, err
	}
	c, _, err := credsFor(ctx, p, project, true)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("http://%s:%d", m.containerHost(ctx, p), FrontPort)
	env := map[string]string{
		"S3_ENDPOINT":             endpoint,
		"S3_REGION":               Region,
		"S3_ACCESS_KEY_ID":        c.AccessKey,
		"S3_SECRET_ACCESS_KEY":    c.Secret,
		"S3_PUBLIC_ENDPOINT":      p.URL(p.Host("s3")),
		"TIFFIN_FILES_URL":        p.URL(p.Host("files")) + "/" + project,
		"AWS_ENDPOINT":            endpoint,
		"AWS_ENDPOINT_URL":        endpoint,
		"AWS_ENDPOINT_URL_S3":     endpoint,
		"AWS_REGION":              Region,
		"AWS_DEFAULT_REGION":      Region,
		"AWS_ACCESS_KEY_ID":       c.AccessKey,
		"AWS_SECRET_ACCESS_KEY":   c.Secret,
		"AWS_S3_FORCE_PATH_STYLE": "true",
	}
	names := make([]string, 0, len(buckets))
	for name := range buckets {
		names = append(names, name)
		env["S3_BUCKET_"+envName(name)] = S3Name(project, name)
	}
	if len(names) == 1 { // Bun.s3's default bucket
		env["S3_BUCKET"] = S3Name(project, names[0])
		env["AWS_BUCKET"] = env["S3_BUCKET"]
	}
	sort.Strings(names)
	var pub []string
	for _, n := range names {
		if buckets[n].Public {
			pub = append(pub, n)
		}
	}
	env["TIFFIN_PUBLIC_BUCKETS"] = strings.Join(pub, ",")
	return env, nil
}

// ---- routes ----

// Routes sends s3.<domain> and files.<domain> to the front server.
func (*Module) Routes(_ context.Context, p *platform.Platform) ([]edge.Route, error) {
	up := fmt.Sprintf("127.0.0.1:%d", FrontPort)
	return []edge.Route{{Host: p.Host("s3"), Upstream: up}, {Host: p.Host("files"), Upstream: up}}, nil
}

// ---- background work and health ----

// Start runs the front server, the usage scanner and the trash purger.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	if _, err := os.Stat(rootEnv(p.DataRoot)); err != nil {
		p.Log.Warn("storage: not installed; skipping", "err", err)
		return nil
	}
	f := &frontServer{m: m, p: p}
	if err := f.start(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.front = f
	m.mu.Unlock()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			m.tracker().refresh(ctx, p)
			if n, err := m.purgeTrash(ctx, p, time.Now().Add(-TrashRetention)); err != nil {
				p.Log.Error("storage: purge trash", "err", err)
			} else if n > 0 {
				p.Log.Info("storage: purged expired trash", "buckets", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-m.tracker().wake:
			}
		}
	}()
	return nil
}

// Checks reports the gateway and front server health.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	gw, err := m.gateway(p)
	if err != nil {
		return []platform.Check{{Name: "storage", OK: false, Detail: err.Error()}}
	}
	if err := gw.healthy(ctx); err != nil {
		return []platform.Check{{Name: "storage", OK: false, Detail: "versitygw is not answering on " + gw.addr + ": " + err.Error()}}
	}
	m.mu.Lock()
	f := m.front
	m.mu.Unlock()
	if f == nil {
		return []platform.Check{{Name: "storage", OK: false, Detail: "the storage front server is not running"}}
	}
	return []platform.Check{{Name: "storage", OK: true, Detail: "versitygw " + GatewayVersion + " on " + gw.addr + ", S3 at " + p.Host("s3")}}
}

// BackupSource is a directory the backup module should mirror off-box.
type BackupSource struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Exclude []string `json:"exclude,omitempty"` // glob patterns relative to Path
	// Manifests holds per-project checksum manifests written by the audit
	// (<project>.json: bucket → key → sha256 and size); a mirror can be
	// verified against them.
	Manifests string `json:"manifests"`
}

// BackupSources describes the object data for an off-box mirror (not yet used):
// what to copy, what to skip (in-flight multipart parts, lock files) and the
// checksum manifests to verify the copy against. Local backup sets already
// include the whole storage tree (see init).
func BackupSources(dataRoot string) []BackupSource {
	return []BackupSource{{
		Name:      "storage",
		Path:      dataDir(dataRoot),
		Exclude:   []string{".vgwlocks", "*/.sgwtmp"},
		Manifests: auditDir(dataRoot),
	}}
}
