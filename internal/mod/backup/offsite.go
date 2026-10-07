package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/version"
)

// Off-box copies: every backup set is copied to an S3-compatible bucket
// (Cloudflare R2, AWS S3, Hetzner Object Storage, MinIO...), so losing the
// server does not lose the data.
//
//   - Postgres: a second pgBackRest repository (repo2, type s3) under
//     <prefix>/pgbackrest, encrypted (aes-256-cbc) with the owner's
//     passphrase, in a config file of its own (offConfPath). Postgres
//     archives WAL to the local repository only, as before; Tiffin ships
//     every archived segment on to repo2 (shipWAL) every few minutes and
//     before a copy counts as done. pgBackRest's own multi-repository
//     archive-push stops pushing to every repository while one fails, so a
//     bucket that is down would hold up local archiving and backups; this
//     way it never touches them. After each local set, an incremental (a
//     full one each week) goes to repo2, with time-based retention.
//   - Everything else in the set (Valkey snapshot, platform state and box
//     key, registered files: buckets, mail, app disk folders...) is uploaded
//     to <prefix>/tiffin/ as encrypted, content-addressed chunks (vault.go).
//
// The passphrase is generated when a destination is first set, shown once,
// and kept sealed on the box. Without it the copies cannot be read, so a
// fresh box (`tiffin backups offsite set ... --passphrase`) needs it to
// restore them. Copies run after the local set, never inside it: a failing
// bucket shows in the status and alerts, while local backups carry on.
const (
	nsOffsite     = "backup.offsite" // "config", "status", "pruned", "notBefore"
	offConfPath   = "/etc/pgbackrest/tiffin-offsite.conf"
	offsiteCAPath = "/etc/pgbackrest/tiffin-offsite-ca.pem"
	// shipPath is where WAL segments wait between the two repositories.
	shipPath = Root + "/wal-ship"
	// OffsiteMaxAge is how old the newest off-box copy may be before the box
	// warns (and the offsite-stale alert fires).
	OffsiteMaxAge = 26 * time.Hour
	// offsiteFullEvery: a full Postgres backup goes to the bucket this often;
	// incrementals in between.
	offsiteFullEvery = 7 * 24 * time.Hour
	retryAfter       = 15 * time.Minute
)

// offsiteRoot keeps the upload caches (on the data disk).
var offsiteRoot = Root + "/offsite"

// Off-box states.
const (
	OffsiteOff     = "off"
	OffsiteActive  = "active"  // copies run
	OffsiteForeign = "foreign" // the bucket holds another Postgres cluster's backups: restore them, or pick another prefix
)

// OffsiteConfig is the stored destination. Secrets are sealed to the box key.
type OffsiteConfig struct {
	Endpoint      string    `json:"endpoint"`
	Region        string    `json:"region"`
	Bucket        string    `json:"bucket"`
	Prefix        string    `json:"prefix"`
	AccessKeyID   string    `json:"accessKeyId"`
	URIStyle      string    `json:"uriStyle"`
	CACert        string    `json:"caCert,omitempty"`
	RetentionDays int       `json:"retentionDays"`
	State         string    `json:"state"`
	SetAt         time.Time `json:"setAt"`
	Sealed        []byte    `json:"sealed"`
}

// offsiteSecrets are sealed in OffsiteConfig.Sealed.
type offsiteSecrets struct {
	SecretAccessKey string       `json:"secretAccessKey"`
	Passphrase      string       `json:"passphrase"`
	Keys            *offsiteKeys `json:"keys"`
}

// dest names a destination, for caches.
func (c *OffsiteConfig) dest() string {
	return c.Endpoint + "|" + c.Bucket + "|" + strings.Trim(c.Prefix, "/")
}

// where is the destination in words.
func (c *OffsiteConfig) where() string {
	u, _ := url.Parse(c.Endpoint)
	host := c.Endpoint
	if u != nil && u.Host != "" {
		host = u.Host
	}
	return "s3://" + c.Bucket + "/" + strings.Trim(c.Prefix, "/") + " at " + host
}

// BackupOffsiteCopy is one off-box copy of a set.
type BackupOffsiteCopy struct {
	Backup        string      `json:"backup" doc:"The backup set copied"`
	Destination   string      `json:"destination,omitempty" doc:"Where it was copied: a copy to an earlier destination does not count for the current one"`
	Status        string      `json:"status" enum:"running,ok,failed"`
	Error         string      `json:"error,omitempty"`
	StartedAt     time.Time   `json:"startedAt"`
	FinishedAt    time.Time   `json:"finishedAt,omitzero"`
	DurationMs    int64       `json:"durationMs"`
	PostgresLabel string      `json:"postgresLabel" doc:"The pgBackRest backup in the bucket (repo2)"`
	PostgresType  string      `json:"postgresType,omitempty" enum:"full,incr,diff,"`
	PostgresBytes int64       `json:"postgresBytes" doc:"What the Postgres backup added to the bucket (compressed)"`
	Files         uploadStats `json:"files" doc:"The set's other parts: Valkey, platform state, registered files"`
	SentBytes     int64       `json:"sentBytes" doc:"Bytes uploaded in all (Postgres plus new chunks)"`
}

// copiedTo reports whether this is a successful copy to destination c. A
// copy to another (earlier) destination is not in c's bucket.
func (cp *BackupOffsiteCopy) copiedTo(c *OffsiteConfig) bool {
	return cp != nil && c != nil && cp.Status == "ok" && cp.Destination == c.where()
}

// offsiteStatus is kept in nsOffsite/"status".
type offsiteStatus struct {
	Last   *BackupOffsiteCopy `json:"last"`
	LastOK *BackupOffsiteCopy `json:"lastOk"`
}

// in memory: the destination (decrypted) for pgBackRest's environment.
var off struct {
	mu      sync.Mutex
	cfg     *OffsiteConfig
	sec     *offsiteSecrets
	poke    chan struct{}
	running string // set being copied
	walErr  string // why the last WAL shipping failed
}

// work serialises off-box operations (copies, prunes, restores from the bucket).
var work sync.Mutex

func loadOffsite(ctx context.Context, p *platform.Platform) (*OffsiteConfig, *offsiteSecrets, error) {
	raw, ok, err := p.DB.KVGet(ctx, nsOffsite, "config")
	if err != nil || !ok {
		return nil, nil, err
	}
	var c OffsiteConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, nil, err
	}
	plain, err := p.Secrets.Unseal(c.Sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("off-box settings: %w", err)
	}
	var s offsiteSecrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, nil, err
	}
	return &c, &s, nil
}

func saveOffsite(ctx context.Context, p *platform.Platform, c *OffsiteConfig, s *offsiteSecrets) error {
	plain, _ := json.Marshal(s)
	sealed, err := p.Secrets.Seal(plain)
	if err != nil {
		return err
	}
	c.Sealed = sealed
	raw, _ := json.Marshal(c)
	if err := p.DB.KVPut(ctx, nsOffsite, "config", raw); err != nil {
		return err
	}
	remember(c, s)
	return nil
}

func remember(c *OffsiteConfig, s *offsiteSecrets) {
	off.mu.Lock()
	off.cfg, off.sec = c, s
	off.mu.Unlock()
}

func current() (*OffsiteConfig, *offsiteSecrets) {
	off.mu.Lock()
	defer off.mu.Unlock()
	return off.cfg, off.sec
}

func getStatus(ctx context.Context, p *platform.Platform) offsiteStatus {
	var s offsiteStatus
	if raw, ok, _ := p.DB.KVGet(ctx, nsOffsite, "status"); ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func putStatus(ctx context.Context, p *platform.Platform, s offsiteStatus) {
	raw, _ := json.Marshal(s)
	_ = p.DB.KVPut(ctx, nsOffsite, "status", raw)
}

// ---- settings ----

// OffsiteInput is what `tiffin backups offsite set` sends.
type OffsiteInput struct {
	Endpoint        string `json:"endpoint" maxLength:"300" doc:"The S3 endpoint, e.g. https://<account>.r2.cloudflarestorage.com, https://s3.eu-central-1.amazonaws.com, https://fsn1.your-objectstorage.com (HTTPS)"`
	Region          string `json:"region,omitempty" maxLength:"64" doc:"Signing region (default: auto for an R2 endpoint, us-east-1 otherwise)"`
	Bucket          string `json:"bucket" maxLength:"63" doc:"Bucket name (it must exist)"`
	Prefix          string `json:"prefix,omitempty" maxLength:"200" doc:"Folder in the bucket for this box (default tiffin); one prefix per box"`
	AccessKeyID     string `json:"accessKeyId" maxLength:"200" doc:"Access key ID"`
	SecretAccessKey string `json:"secretAccessKey,omitempty" maxLength:"300" doc:"Secret access key (stored sealed with the box key; never shown again). Optional when changing other settings of the same destination"`
	URIStyle        string `json:"uriStyle,omitempty" enum:"path,host," doc:"path (default: https://endpoint/bucket/key, works everywhere) or host (https://bucket.endpoint/key)"`
	CACert          string `json:"caCert,omitempty" maxLength:"20000" doc:"PEM certificate of a private CA that signs the endpoint's certificate (self-hosted MinIO)"`
	RetentionDays   int    `json:"retentionDays,omitempty" minimum:"0" maximum:"3650" doc:"Days of off-box copies to keep (default 30)"`
	Passphrase      string `json:"passphrase,omitempty" maxLength:"200" doc:"The passphrase of copies already at this destination (to restore them on a fresh box). Leave empty to have one generated for a new destination"`
}

func normalize(in OffsiteInput, prev *OffsiteConfig) (*OffsiteConfig, error) {
	ep := strings.TrimSpace(in.Endpoint)
	if ep == "" {
		return nil, errors.New("endpoint is required")
	}
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("endpoint %q is not a URL like https://s3.example.com", in.Endpoint)
	}
	if u.Scheme != "https" {
		return nil, errors.New("the endpoint must be HTTPS: pgBackRest only talks to S3 over TLS")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("the endpoint is the server only (https://host[:port]); put the bucket in bucket and a folder in prefix")
	}
	c := &OffsiteConfig{Endpoint: "https://" + u.Host, Region: strings.TrimSpace(in.Region), Bucket: strings.TrimSpace(in.Bucket),
		Prefix: strings.Trim(strings.TrimSpace(in.Prefix), "/"), AccessKeyID: strings.TrimSpace(in.AccessKeyID), URIStyle: in.URIStyle,
		CACert: strings.TrimSpace(in.CACert), RetentionDays: in.RetentionDays}
	if c.Region == "" {
		c.Region = "us-east-1"
		if strings.HasSuffix(u.Hostname(), ".r2.cloudflarestorage.com") {
			c.Region = "auto" // R2 signs for "auto"
		}
	}
	if c.Prefix == "" {
		c.Prefix = "tiffin"
	}
	if c.URIStyle == "" {
		c.URIStyle = "path"
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = 30
		if prev != nil && prev.RetentionDays > 0 {
			c.RetentionDays = prev.RetentionDays
		}
	}
	if c.Bucket == "" || c.AccessKeyID == "" {
		return nil, errors.New("bucket and accessKeyId are required")
	}
	for _, r := range c.Prefix {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./", r)) {
			return nil, errors.New("prefix may hold letters, digits, - _ . and /")
		}
	}
	if strings.Contains(c.Prefix, "..") || strings.Contains(c.Prefix, "//") {
		return nil, errors.New("prefix must be a plain path like tiffin/shop-box")
	}
	return c, nil
}

// ProbeStep is one step of a destination test.
type ProbeStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Ms     int64  `json:"ms"`
	Detail string `json:"detail,omitempty"`
}

// probe writes, reads back and deletes a small random object.
func probe(ctx context.Context, st objectStore, prefix string) ([]ProbeStep, error) {
	var steps []ProbeStep
	body := make([]byte, 64)
	_, _ = rand.Read(body)
	key := vaultPrefix(prefix) + "probe-" + hex.EncodeToString(body[:6])
	step := func(name string, f func() error) error {
		t := time.Now()
		err := f()
		s := ProbeStep{Name: name, OK: err == nil, Ms: time.Since(t).Milliseconds()}
		if err != nil {
			s.Detail = err.Error()
		}
		steps = append(steps, s)
		return err
	}
	if err := step("write "+key, func() error { return st.Put(ctx, key, body) }); err != nil {
		return steps, fmt.Errorf("writing a test object failed: %w", err)
	}
	err := step("read it back", func() error {
		got, err := st.Get(ctx, key, maxKeyObject)
		if err == nil && string(got) != string(body) {
			err = errors.New("the object read back differs from what was written")
		}
		return err
	})
	derr := step("delete it", func() error { return st.Delete(ctx, key) })
	if err != nil {
		return steps, fmt.Errorf("reading the test object back failed: %w", err)
	}
	if derr != nil {
		return steps, fmt.Errorf("deleting the test object failed (copies need delete to apply retention): %w", derr)
	}
	return steps, nil
}

// ---- pgBackRest repo2 ----

// repo2Options are pgBackRest's settings for the off-box repository.
func repo2Options(c *OffsiteConfig, s *offsiteSecrets) [][2]string {
	u, _ := url.Parse(c.Endpoint)
	host, port := u.Host, ""
	if h, pt, err := net.SplitHostPort(u.Host); err == nil {
		host, port = h, pt
	}
	kv := [][2]string{
		{"repo2-type", "s3"},
		{"repo2-path", "/" + strings.Trim(c.Prefix, "/") + "/pgbackrest"},
		{"repo2-s3-bucket", c.Bucket},
		{"repo2-s3-endpoint", host},
		{"repo2-s3-region", c.Region},
		{"repo2-s3-key", c.AccessKeyID},
		{"repo2-s3-key-secret", s.SecretAccessKey},
		{"repo2-s3-uri-style", c.URIStyle},
		{"repo2-cipher-type", "aes-256-cbc"},
		{"repo2-cipher-pass", s.Passphrase},
		{"repo2-retention-full-type", "time"},
		{"repo2-retention-full", strconv.Itoa(c.RetentionDays)},
		{"repo2-bundle", "y"},
		// A restore reads each bundled file with a request of its own, and a
		// bundle is one worker's job: small bundles spread those requests
		// over the restore's workers (the default 20MiB put a cluster's
		// ~600 small files in one, 46 s of serial requests to R2).
		{"repo2-bundle-size", "2MiB"},
		{"repo2-block", "y"},
	}
	if port != "" && port != "443" {
		kv = append(kv, [2]string{"repo2-storage-port", port})
	}
	if c.CACert != "" {
		kv = append(kv, [2]string{"repo2-storage-ca-file", offsiteCAPath})
	}
	return kv
}

// offConf is the off-box repository's own config file. Only Tiffin's
// commands read it (pgbackrestOff); Postgres's archive_command reads
// pgbackrest.conf, so archiving never waits on the bucket.
func offConf(c *OffsiteConfig, s *offsiteSecrets) string {
	var b strings.Builder
	b.WriteString("# Managed by Tiffin (tiffin backups offsite set): the off-box repository.\n# Holds credentials: root and postgres only.\n[global]\n")
	for _, kv := range repo2Options(c, s) {
		b.WriteString(kv[0] + "=" + kv[1] + "\n")
	}
	b.WriteString("start-fast=y\ncompress-type=zst\ncompress-level=3\nprocess-max=2\nlog-level-console=warn\nlog-level-file=info\nlog-path=" + LogPath + "\n\n")
	// Restores (and drills) from the bucket wait on requests, not the CPU.
	b.WriteString("[global:restore]\nprocess-max=8\n\n")
	b.WriteString("[" + Stanza + "]\npg1-path=" + postgres.DataDir + "\npg1-socket-path=" + postgres.SocketDir + "\npg1-port=5432\n")
	return b.String()
}

// writeOffConf installs the off-box repository's config file (and the CA
// that signs the endpoint, if any), or with c nil removes them.
func writeOffConf(c *OffsiteConfig, s *offsiteSecrets) error {
	if c == nil {
		for _, f := range []string{offConfPath, offsiteCAPath} {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	uid, gid, err := pgIDs()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(shipPath, 0o700); err != nil {
		return err
	}
	if err := os.Chown(shipPath, uid, gid); err != nil {
		return err
	}
	if c.CACert != "" {
		if err := os.WriteFile(offsiteCAPath, []byte(c.CACert+"\n"), 0o644); err != nil {
			return err
		}
	}
	tmp := offConfPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(offConf(c, s)), 0o640); err != nil {
		return err
	}
	if err := os.Chown(tmp, 0, gid); err != nil {
		return err
	}
	return os.Rename(tmp, offConfPath)
}

// pgbackrestOff runs pgBackRest on the off-box repository.
func pgbackrestOff(ctx context.Context, args ...string) (string, error) {
	return asUser(ctx, "postgres", "pgbackrest", append([]string{"--config=" + offConfPath, "--stanza=" + Stanza}, args...)...)
}

// stanzaMismatch reports a pgBackRest error saying the repository belongs
// to another cluster.
func stanzaMismatch(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "do not match") || strings.Contains(s, "[028]")
}

// activate creates (or checks) the stanza in repo2: it decides whether this
// box's cluster owns the destination.
func activate(ctx context.Context) (string, error) {
	_, err := pgbackrestOff(ctx, "--log-level-console=warn", "stanza-create")
	if stanzaMismatch(err) {
		return OffsiteForeign, nil
	}
	if err != nil {
		return "", fmt.Errorf("pgBackRest could not use the bucket: %s", clean(err))
	}
	return OffsiteActive, nil
}

// walName is the WAL file a file in a pgBackRest archive holds: a segment
// ("000000010000000000000009-<sha1>.zst") or a timeline history
// ("00000002.history"). Backup labels and partial segments are not shipped.
func walName(file string) (string, bool) {
	hex := func(s string) bool {
		for _, r := range s {
			if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'F') {
				return false
			}
		}
		return true
	}
	if len(file) == 16 && strings.HasSuffix(file, ".history") && hex(file[:8]) {
		return file, true
	}
	if len(file) >= 24 && hex(file[:24]) && (len(file) == 24 || file[24] == '-') {
		return file[:24], true
	}
	return "", false
}

// localWAL lists the WAL in the local repository's archive, sorted.
func localWAL() []string {
	var out []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(RepoPath, "archive", Stanza), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if n, ok := walName(d.Name()); ok && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// remoteWAL lists the WAL in the off-box repository's archive.
func remoteWAL(ctx context.Context) (map[string]bool, error) {
	out, err := asUser(ctx, "postgres", "pgbackrest", "--config="+offConfPath, "--repo=2", "--recurse", "repo-ls", "archive/"+Stanza)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if n, ok := walName(path.Base(strings.TrimSpace(l))); ok {
			have[n] = true
		}
	}
	return have, nil
}

// shipWAL copies the archived WAL the bucket's repository lacks to it,
// oldest first, from the first segment its oldest backup needs (timeline
// histories always). It stops at the first failure.
func shipWAL(ctx context.Context) (int, error) {
	info, err := repoInfoOf(ctx, 2)
	if err != nil || len(info) == 0 {
		return 0, err
	}
	from := info[0].Archive.Start
	have, err := remoteWAL(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, name := range localWAL() {
		if have[name] || (!strings.HasSuffix(name, ".history") && name < from) {
			continue
		}
		tmp := filepath.Join(shipPath, name)
		_ = os.Remove(tmp)
		if _, err := pgbackrest(ctx, "--repo=1", "--log-level-console=warn", "archive-get", name, tmp); err != nil {
			return n, fmt.Errorf("reading WAL %s from the local repository: %s", name, clean(err))
		}
		_, err := pgbackrestOff(ctx, "--log-level-console=error", "archive-push", tmp)
		_ = os.Remove(tmp)
		if err != nil {
			return n, fmt.Errorf("shipping WAL %s to the bucket: %s", name, clean(err))
		}
		n++
	}
	return n, nil
}

// ensureWAL waits until the local archive has segment stop, ships WAL, and
// checks the bucket's repository has every segment from start to stop: a
// backup there restores only with them.
func ensureWAL(ctx context.Context, start, stop string) error {
	deadline := time.Now().Add(5 * time.Minute)
	for !slices.Contains(localWAL(), stop) {
		if time.Now().After(deadline) {
			return fmt.Errorf("WAL segment %s did not reach the local archive in 5 minutes (is archiving working? `tiffin status`)", stop)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if _, err := shipWAL(ctx); err != nil {
		return err
	}
	have, err := remoteWAL(ctx)
	if err != nil {
		return err
	}
	for _, name := range localWAL() {
		if len(name) == 24 && name >= start && name <= stop && !have[name] {
			return fmt.Errorf("WAL segment %s, which the backup needs, is not in the bucket", name)
		}
	}
	return nil
}

// ---- copies ----

// fileCache is what earlier uploads learnt: the chunks the destination has.
type fileCache struct {
	Dest     string    `json:"dest"`
	ListedAt time.Time `json:"listedAt"`
	Chunks   []string  `json:"chunks"`
}

func cachePath() string { return filepath.Join(offsiteRoot, "cache.json") }

func loadCache(c *OffsiteConfig) *fileCache {
	var fc fileCache
	if raw, err := os.ReadFile(cachePath()); err == nil && json.Unmarshal(raw, &fc) == nil && fc.Dest == c.dest() {
		return &fc
	}
	return &fileCache{Dest: c.dest()}
}

func saveCache(fc *fileCache, known map[string]bool) error {
	fc.Chunks = fc.Chunks[:0]
	for k := range known {
		fc.Chunks = append(fc.Chunks, k)
	}
	slices.Sort(fc.Chunks)
	raw, _ := json.Marshal(fc)
	if err := os.MkdirAll(offsiteRoot, 0o700); err != nil {
		return err
	}
	tmp := cachePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath())
}

// knownChunks is the destination's chunk list: the cache, or a fresh
// listing when the cache is a day old.
func knownChunks(ctx context.Context, v *vault, fc *fileCache) (map[string]bool, error) {
	if time.Since(fc.ListedAt) < 24*time.Hour {
		m := make(map[string]bool, len(fc.Chunks))
		for _, c := range fc.Chunks {
			m[c] = true
		}
		return m, nil
	}
	m, err := v.chunkIDs(ctx)
	if err != nil {
		return nil, err
	}
	fc.ListedAt = time.Now()
	return m, nil
}

// refsPath keeps the chunk IDs of a set this box uploaded, so pruning does
// not download trees.
// refsPath is a set's cached chunk list. "refs2": lists written before they
// were written atomically may be cut short, so they are read again from the
// bucket rather than trusted.
func refsPath(id string) string { return filepath.Join(offsiteRoot, "refs2", id) }

func openVault(c *OffsiteConfig, s *offsiteSecrets) (*vault, error) {
	st, err := newS3(c, s.SecretAccessKey)
	if err != nil {
		return nil, err
	}
	return newVault(st, c.Prefix, s.Keys)
}

// ErrOffsiteOff is returned when no destination is set (or it is not active).
var ErrOffsiteOff = errors.New("copies off the box are off; set a destination with `tiffin backups offsite set`")

// CopyOffsite copies a backup set off the box: Postgres to repo2, the rest
// to the bucket. It waits for a running copy to finish first; a set copied
// to this destination already returns that copy.
func CopyOffsite(ctx context.Context, p *platform.Platform, b *Backup) (*BackupOffsiteCopy, error) {
	work.Lock()
	defer work.Unlock()
	c, s := current()
	if c == nil {
		return nil, ErrOffsiteOff
	}
	if cur, err := get(ctx, p, b.ID); err == nil && cur.Offsite.copiedTo(c) {
		return cur.Offsite, nil
	}
	if c.State != OffsiteActive {
		return nil, fmt.Errorf("copies are paused: %s", foreignWords)
	}
	cp := &BackupOffsiteCopy{Backup: b.ID, Destination: c.where(), Status: "running", StartedAt: time.Now().UTC()}
	off.mu.Lock()
	off.running = b.ID
	off.mu.Unlock()
	defer func() {
		off.mu.Lock()
		off.running = ""
		off.mu.Unlock()
	}()
	err := copyParts(ctx, p, c, s, b, cp)
	cp.FinishedAt = time.Now().UTC()
	cp.DurationMs = cp.FinishedAt.Sub(cp.StartedAt).Milliseconds()
	cp.Status = "ok"
	if err != nil {
		cp.Status, cp.Error = "failed", err.Error()
		p.Log.Error("backup: off-box copy failed", "backup", b.ID, "err", err)
	} else {
		p.Log.Info("backup: copied off the box", "backup", b.ID, "label", cp.PostgresLabel, "sent", cp.SentBytes, "ms", cp.DurationMs)
	}
	bg := context.WithoutCancel(ctx)
	st := getStatus(bg, p)
	st.Last = cp
	if err == nil {
		st.LastOK = cp
	}
	putStatus(bg, p, st)
	if cur, gerr := get(bg, p, b.ID); gerr == nil {
		cur.Offsite = cp
		_ = save(bg, p, cur)
	}
	return cp, err
}

func copyParts(ctx context.Context, p *platform.Platform, c *OffsiteConfig, s *offsiteSecrets, b *Backup, cp *BackupOffsiteCopy) error {
	if b.Status != "ok" {
		return errors.New("only successful backups are copied")
	}
	if why := uncopyable(ctx, p, b, time.Now()); why != "" {
		return errors.New(why)
	}
	if _, err := os.Stat(b.dir()); err != nil {
		return fmt.Errorf("the set's files are gone from this box (%w)", err)
	}
	v, err := openVault(c, s)
	if err != nil {
		return err
	}
	// A quick look first: pgBackRest takes minutes to give up on a bucket it cannot reach.
	if _, err := v.st.Get(ctx, v.keyKey(), maxKeyObject); err != nil {
		return fmt.Errorf("the bucket cannot be read: %w", err)
	}
	// 1. Postgres: a backup to repo2, between local backups (pgBackRest
	// runs one backup of a cluster at a time).
	typ := "incr"
	if info, err := repoInfoOf(ctx, 2); err == nil {
		var lastFull time.Time
		for _, x := range info {
			if x.Type == "full" {
				lastFull = time.Unix(x.Timestamp.Stop, 0)
			}
		}
		if time.Since(lastFull) >= offsiteFullEvery {
			typ = "full"
		}
	}
	release, err := waitRun(ctx, 30*time.Minute)
	if err != nil {
		return err
	}
	if why := uncopyable(ctx, p, b, time.Now()); why != "" {
		release() // a restore ran meanwhile
		return errors.New(why)
	}
	// No archive check: the backup's WAL reaches the bucket through
	// shipWAL, checked below, not through Postgres's archive_command.
	_, err = pgbackrestOff(ctx, "--repo=2", "--type="+typ, "--no-archive-check", "--log-level-console=warn", "backup")
	release()
	if err != nil {
		return fmt.Errorf("postgres: %s", clean(err))
	}
	info, err := repoInfoOf(ctx, 2)
	if err != nil || len(info) == 0 {
		return fmt.Errorf("postgres: pgBackRest lists no backup in the bucket after backing up (%v)", err)
	}
	last := info[len(info)-1]
	if err := ensureWAL(ctx, last.Archive.Start, last.Archive.Stop); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	cp.PostgresLabel, cp.PostgresType, cp.PostgresBytes = last.Label, last.Type, last.Info.Repository.Delta
	cp.SentBytes = cp.PostgresBytes

	// 2. The rest of the set, as chunks.
	fc := loadCache(c)
	known, err := knownChunks(ctx, v, fc)
	if err != nil {
		return fmt.Errorf("listing the bucket: %w", err)
	}
	rec := &offsiteSet{Backup: *b, Box: hostname(), Version: version.Version}
	rec.Backup.Offsite = &BackupOffsiteCopy{Backup: b.ID, Status: "ok", PostgresLabel: cp.PostgresLabel, PostgresType: cp.PostgresType}
	entries, err := v.putSet(ctx, rec, b.dir(), known)
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}
	cp.Files = rec.Upload
	cp.SentBytes += rec.Upload.SentBytes
	if err := saveCache(fc, known); err != nil {
		p.Log.Warn("backup: saving the off-box upload cache", "err", err)
	}
	saveRefs(b.ID, treeChunks(entries))
	return nil
}

// offsiteMaxLag is how old a set may be when it is copied: its Postgres
// part is backed up to the bucket when the copy runs, so the set's other
// parts must not be much older than that.
const offsiteMaxLag = 6 * time.Hour

// uncopyable says why set b cannot be copied now ("" when it can): it is
// too old, or older than the newest restore (its files are from before it).
func uncopyable(ctx context.Context, p *platform.Platform, b *Backup, now time.Time) string {
	if b.Trigger == "pre-restore" {
		return "backup " + b.ID + " is the safety backup of a restore; it stays on this box"
	}
	if now.Sub(b.StartedAt) > offsiteMaxLag {
		return fmt.Sprintf("backup %s is %s old; a copy pairs a set with the database as it is when the copy runs, so only sets of the last 6 hours are copied (take a new one with `tiffin backup`)", b.ID, ago(now.Sub(b.StartedAt)))
	}
	if raw, ok, _ := p.DB.KVGet(ctx, nsOffsite, "notBefore"); ok {
		if t, err := time.Parse(time.RFC3339Nano, string(raw)); err == nil && b.StartedAt.Before(t) {
			return "backup " + b.ID + " was taken before the last restore; take a new one with `tiffin backup`"
		}
	}
	return ""
}

// saveRefs caches the chunks a set refers to. Pruning trusts the cache, so
// it is whole or absent (and then read from the bucket): a file cut short by
// a full disk would let pruning delete chunks the set still needs.
func saveRefs(id string, refs []string) {
	path := refsPath(id)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	_, err = f.WriteString(strings.Join(refs, "\n"))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		_ = os.Remove(path)
	}
}

// waitRun takes the backup lock, waiting up to d for a running backup.
func waitRun(ctx context.Context, d time.Duration) (func(), error) {
	deadline := time.Now().Add(d)
	for {
		if run.TryLock() {
			return run.Unlock, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("a backup, restore or drill kept running; try again later")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// repoInfoOf is `pgbackrest info` for one repository.
func repoInfoOf(ctx context.Context, repo int) ([]repoBackup, error) {
	run := pgbackrest
	if repo == 2 {
		run = pgbackrestOff
	}
	out, err := run(ctx, "--repo="+strconv.Itoa(repo), "--output=json", "info")
	if err != nil {
		return nil, err
	}
	var stanzas []struct {
		Name   string       `json:"name"`
		Backup []repoBackup `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &stanzas); err != nil {
		return nil, fmt.Errorf("parse pgbackrest info: %w", err)
	}
	for _, s := range stanzas {
		if s.Name == Stanza {
			return s.Backup, nil
		}
	}
	return nil, nil
}

// pruneOffsite drops sets older than the retention (and sets whose
// Postgres backup pgBackRest has expired), then chunks nothing uses.
func pruneOffsite(ctx context.Context, p *platform.Platform) (pruneResult, error) {
	work.Lock()
	defer work.Unlock()
	c, s := current()
	if c == nil || c.State != OffsiteActive {
		return pruneResult{}, ErrOffsiteOff
	}
	v, err := openVault(c, s)
	if err != nil {
		return pruneResult{}, err
	}
	info, ierr := repoInfoOf(ctx, 2)
	cutoff := time.Now().Add(-time.Duration(c.RetentionDays) * 24 * time.Hour)
	res, err := v.prune(ctx, pruneDrop(ctx, v, c, func(id string) (*Backup, error) { return get(ctx, p, id) }, info, ierr, cutoff),
		func(id string) ([]string, error) {
			if raw, err := os.ReadFile(refsPath(id)); err == nil {
				return strings.Fields(string(raw)), nil
			}
			e, err := v.getEntries(ctx, id)
			if err != nil {
				return nil, err
			}
			refs := treeChunks(e)
			saveRefs(id, refs)
			return refs, nil
		})
	for _, id := range res.Sets {
		_ = os.Remove(refsPath(id))
	}
	if err == nil {
		// The next upload lists the bucket again.
		fc := loadCache(c)
		fc.ListedAt = time.Time{}
		_ = saveCache(fc, map[string]bool{})
		_ = p.DB.KVPut(ctx, nsOffsite, "pruned", []byte(time.Now().UTC().Format(time.RFC3339)))
		if len(res.Sets) > 0 || res.Chunks > 0 {
			p.Log.Info("backup: pruned off-box copies", "sets", len(res.Sets), "chunks", res.Chunks)
		}
	}
	return res, err
}

// pruneDrop decides which sets a prune drops: those older than cutoff, and,
// when pgBackRest listed repo2 (info, ierr), those whose Postgres backup it
// has expired. A set's label comes from its local record (local) or else
// its record in the bucket; one that cannot be read is an error, never an
// empty label: an empty label would drop a set that is still good.
func pruneDrop(ctx context.Context, v *vault, c *OffsiteConfig, local func(id string) (*Backup, error), info []repoBackup, ierr error, cutoff time.Time) func(string) (bool, error) {
	labels := map[string]bool{}
	for _, x := range info {
		labels[x.Label] = true
	}
	return func(id string) (bool, error) {
		if t, ok := ulidTime(id); ok && t.Before(cutoff) {
			return true, nil
		}
		if ierr != nil || len(info) == 0 {
			return false, nil
		}
		b, err := local(id)
		switch {
		case err == nil && b.Offsite.copiedTo(c) && b.Offsite.PostgresLabel != "":
			return !labels[b.Offsite.PostgresLabel], nil
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return false, err
		}
		rec, err := v.getSet(ctx, id)
		if err != nil {
			return false, fmt.Errorf("reading its record: %w", err)
		}
		if rec.Backup.Offsite == nil || rec.Backup.Offsite.PostgresLabel == "" {
			return true, nil // no Postgres backup: nothing can restore it
		}
		return !labels[rec.Backup.Offsite.PostgresLabel], nil
	}
}

// offsiteLoop copies the newest backup set off the box once it exists
// (retrying a failed copy after 15 minutes), prunes once a day, and checks
// a foreign destination again (it becomes this box's once its backups are
// restored here).
func offsiteLoop(ctx context.Context, p *platform.Platform) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-off.poke:
		}
		timer.Reset(5 * time.Minute)
		c, _ := current()
		if c != nil && c.State == OffsiteForeign {
			recheck(ctx, p)
			c, _ = current()
		}
		if c == nil || c.State != OffsiteActive {
			continue
		}
		list, err := List(ctx, p)
		if err != nil {
			continue
		}
		if b := lastOK(list, ""); b != nil && uncopyable(ctx, p, b, time.Now()) == "" && copyDue(b.Offsite, c, time.Now()) {
			_, _ = CopyOffsite(ctx, p, b)
		}
		// WAL archived since the last copy follows it to the bucket.
		if work.TryLock() {
			n, err := 0, reachable(ctx)
			if err == nil {
				n, err = shipWAL(ctx)
			}
			work.Unlock()
			off.mu.Lock()
			off.walErr = ""
			if err != nil {
				off.walErr = err.Error()
			}
			off.mu.Unlock()
			if err != nil {
				p.Log.Warn("backup: shipping WAL off the box", "err", err)
			} else if n > 0 {
				p.Log.Info("backup: shipped WAL off the box", "files", n)
			}
		}
		var last time.Time
		if raw, ok, _ := p.DB.KVGet(ctx, nsOffsite, "pruned"); ok {
			last, _ = time.Parse(time.RFC3339, string(raw))
		}
		if time.Since(last) >= 24*time.Hour && getStatus(ctx, p).LastOK != nil {
			if _, err := pruneOffsite(ctx, p); err != nil && !errors.Is(err, ErrOffsiteOff) {
				p.Log.Warn("backup: pruning off-box copies", "err", err)
			}
		}
	}
}

// copyDue says whether a set whose last copy attempt was last should be
// copied to c now: never copied there, or that copy failed 15 minutes ago.
func copyDue(last *BackupOffsiteCopy, c *OffsiteConfig, now time.Time) bool {
	if last == nil || last.Destination != c.where() {
		return true
	}
	return last.Status == "failed" && now.Sub(last.FinishedAt) >= retryAfter
}

// reachable reads the destination's key bundle: a quick check before
// pgBackRest, which takes minutes to give up on a bucket it cannot reach.
func reachable(ctx context.Context) error {
	c, s := current()
	if c == nil {
		return ErrOffsiteOff
	}
	v, err := openVault(c, s)
	if err == nil {
		_, err = v.st.Get(ctx, v.keyKey(), maxKeyObject)
	}
	if err != nil {
		return fmt.Errorf("the bucket cannot be read: %w", err)
	}
	return nil
}

// recheck asks pgBackRest again whether a foreign destination's stanza
// matches the cluster now, and turns copies on when it does.
func recheck(ctx context.Context, p *platform.Platform) {
	if !work.TryLock() {
		return
	}
	defer work.Unlock()
	c, s := current()
	if c == nil || c.State != OffsiteForeign {
		return
	}
	state, err := activate(ctx)
	if err != nil || state != OffsiteActive {
		return
	}
	n := *c
	n.State = OffsiteActive
	if err := saveOffsite(ctx, p, &n, s); err == nil {
		p.Log.Info("backup: the off-box destination holds this cluster's backups now; copies resume")
	}
}

// pokeOffsite asks the loop to look now (after a backup).
func pokeOffsite() {
	select {
	case off.poke <- struct{}{}:
	default:
	}
}

// startOffsite loads the destination and starts the loop.
func startOffsite(ctx context.Context, p *platform.Platform) {
	off.mu.Lock()
	if off.poke == nil {
		off.poke = make(chan struct{}, 1)
	}
	off.mu.Unlock()
	c, s, err := loadOffsite(ctx, p)
	if err != nil {
		p.Log.Error("backup: reading the off-box settings", "err", err)
	}
	remember(c, s)
	// The config file follows the stored settings (a restore may have
	// swapped the state in).
	if err := writeOffConf(c, s); err != nil {
		p.Log.Error("backup: writing pgBackRest's off-box settings", "err", err)
	}
	go offsiteLoop(ctx, p)
}

const foreignWords = "this bucket prefix holds the backups of another Postgres cluster (another box's): restore them with `tiffin restore latest --from offsite`, or choose another prefix for this box's copies"

// ---- status ----

// BackupOffsite is the off-box copy setting and how it is going.
type BackupOffsite struct {
	Enabled       bool               `json:"enabled" doc:"Copies off the box are on"`
	State         string             `json:"state" enum:"off,active,foreign" doc:"off; active (copies run); foreign (the destination holds another cluster's backups: restore them, or choose another prefix)"`
	Endpoint      string             `json:"endpoint,omitempty"`
	Region        string             `json:"region,omitempty"`
	Bucket        string             `json:"bucket,omitempty"`
	Prefix        string             `json:"prefix,omitempty"`
	AccessKeyID   string             `json:"accessKeyId,omitempty"`
	URIStyle      string             `json:"uriStyle,omitempty"`
	CustomCA      bool               `json:"customCa,omitempty"`
	RetentionDays int                `json:"retentionDays,omitempty"`
	SetAt         *time.Time         `json:"setAt,omitempty"`
	Copying       string             `json:"copying,omitempty" doc:"The set being copied now"`
	LastCopy      *BackupOffsiteCopy `json:"lastCopy" doc:"The newest copy attempt"`
	LastOK        *BackupOffsiteCopy `json:"lastOk" doc:"The newest successful copy"`
	LastOKAt      *time.Time         `json:"lastOkAt" doc:"When the newest successful copy finished"`
	Message       string             `json:"message" doc:"How it is going, in plain words"`
	// Only in the answer to `offsite set`, once.
	Passphrase     string `json:"passphrase,omitempty" doc:"Shown once, when a new destination is set: keep it somewhere safe, off this box"`
	PassphraseNote string `json:"passphraseNote,omitempty"`
}

func offsiteView(ctx context.Context, p *platform.Platform) *BackupOffsite {
	c, _ := current()
	v := &BackupOffsite{State: OffsiteOff, Message: "Off: backups are only on this server. Losing the server loses them; set a destination with `tiffin backups offsite set`."}
	if c == nil {
		return v
	}
	st := getStatus(ctx, p)
	t := c.SetAt
	v.Enabled, v.State, v.Endpoint, v.Region, v.Bucket, v.Prefix, v.AccessKeyID, v.URIStyle = true, c.State, c.Endpoint, c.Region, c.Bucket, c.Prefix, c.AccessKeyID, c.URIStyle
	v.CustomCA, v.RetentionDays, v.SetAt, v.LastCopy, v.LastOK = c.CACert != "", c.RetentionDays, &t, st.Last, st.LastOK
	off.mu.Lock()
	v.Copying = off.running
	off.mu.Unlock()
	if st.LastOK != nil {
		f := st.LastOK.FinishedAt
		v.LastOKAt = &f
	}
	v.Message = offsiteWords(c, st, time.Now())
	return v
}

func offsiteWords(c *OffsiteConfig, st offsiteStatus, now time.Time) string {
	if c.State == OffsiteForeign {
		return "Paused: " + foreignWords + "."
	}
	where := c.where()
	switch {
	case st.LastOK == nil && st.Last != nil && st.Last.Status == "failed":
		return "On, copying to " + where + ", but no copy has succeeded yet: " + st.Last.Error
	case st.LastOK == nil:
		return "On, copying to " + where + "; the first copy runs after the next backup (or `tiffin backups offsite copy`)."
	}
	msg := fmt.Sprintf("On: copied to %s %s ago (%s sent).", where, ago(now.Sub(st.LastOK.FinishedAt)), human(st.LastOK.SentBytes))
	if st.Last != nil && st.Last.Status == "failed" && st.Last.StartedAt.After(st.LastOK.StartedAt) {
		msg += " The latest copy failed: " + st.Last.Error
	}
	off.mu.Lock()
	walErr := off.walErr
	off.mu.Unlock()
	if walErr != "" {
		msg += " Shipping WAL fails (it catches up later): " + walErr
	}
	return msg
}

// offsiteCheck is the "offsite-backups" status check.
func offsiteCheck(ctx context.Context, p *platform.Platform, now time.Time) platform.Check {
	ch := platform.Check{Name: "offsite-backups", OK: true}
	c, _ := current()
	if c == nil {
		ch.Detail = "off: backups are only on this server; set a destination with `tiffin backups offsite set`"
		return ch
	}
	st := getStatus(ctx, p)
	ch.Detail = offsiteWords(c, st, now)
	switch {
	case c.State == OffsiteForeign:
		ch.OK = false
	case st.LastOK != nil:
		ch.OK = now.Sub(st.LastOK.FinishedAt) <= OffsiteMaxAge
		if !ch.OK {
			ch.Detail += " That is more than 26 hours ago: check `tiffin backups offsite show` and `tiffin backups offsite test`."
		}
	default:
		ch.OK = now.Sub(c.SetAt) <= OffsiteMaxAge
	}
	return ch
}

// OffsiteAge is for the offsite-stale alert: hours since the newest
// successful off-box copy (since the destination was set, before the
// first). on is false when copies are off.
func OffsiteAge(ctx context.Context, p *platform.Platform) (hours float64, on bool, summary string) {
	c, _ := current()
	if c == nil || p == nil || p.DB == nil {
		return 0, false, ""
	}
	st := getStatus(ctx, p)
	since := c.SetAt
	if st.LastOK != nil {
		since = st.LastOK.FinishedAt
	}
	h := time.Since(since).Hours()
	return h, true, offsiteWords(c, st, time.Now())
}

// LastDrillFailed is for the restore-drill alert: whether the newest
// finished drill failed, and its message.
func LastDrillFailed(ctx context.Context, p *platform.Platform) (failed, any bool, summary string) {
	if p == nil || p.DB == nil {
		return false, false, ""
	}
	drills, err := ListDrills(ctx, p)
	if err != nil {
		return false, false, ""
	}
	for _, d := range drills {
		if d.Status == DrillRunning {
			continue
		}
		where := "local"
		if d.Source == SourceOffsite {
			where = "off-box"
		}
		if d.Status == DrillFailed {
			return true, true, "the last restore drill (" + where + " copy of " + d.Backup + ") failed: " + clipText(d.Message, 300)
		}
		return false, true, "the last restore drill (" + where + " copy of " + d.Backup + ") passed"
	}
	return false, false, ""
}
