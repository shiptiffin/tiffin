package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/klauspost/compress/zstd"
)

// The off-box copy of everything in a backup set besides Postgres (which
// pgBackRest copies to its own repo2) lives under <prefix>/tiffin/ in the
// bucket:
//
//	key                      the key bundle, sealed with the owner's passphrase (age scrypt)
//	sets/<bk_id>/info        the set's record (Backup), sealed to the bundle's key
//	sets/<bk_id>/tree        every file of the set: metadata and chunk IDs, sealed
//	chunks/<ab>/<id>         file content in pieces of up to 4 MiB: zstd, then age
//
// A chunk's ID is HMAC-SHA256 of its plain content under a key in the
// bundle: the same content is stored once however many sets and files hold
// it, and the names say nothing about the content to whoever can list the
// bucket. A set is uploaded by walking its directory: files whose size and
// time match the previous upload reuse its chunk IDs without being read,
// and only chunks the bucket lacks are sent.
const (
	chunkSize    = 4 << 20
	vaultVersion = 1
	putWorkers   = 4
	getWorkers   = 8
)

// offsiteKeys is the key bundle (the "key" object).
type offsiteKeys struct {
	Version  int       `json:"v"`
	Identity string    `json:"identity"` // age X25519 secret key: decrypts sets and chunks
	MAC      []byte    `json:"mac"`      // names chunks
	Created  time.Time `json:"created"`
	Box      string    `json:"box,omitempty"`
}

func newKeys(box string) (*offsiteKeys, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	mac := make([]byte, 32)
	if _, err := rand.Read(mac); err != nil {
		return nil, err
	}
	return &offsiteKeys{Version: vaultVersion, Identity: id.String(), MAC: mac, Created: time.Now().UTC(), Box: box}, nil
}

// newPassphrase is 30 random characters in six groups ("k4wq9-..."), about
// 150 bits: the owner writes it down; pgBackRest and the key bundle use it.
func newPassphrase() string {
	const alpha = "abcdefghijkmnpqrstuvwxyz23456789" // no 0/o, 1/l
	b := make([]byte, 30)
	_, _ = rand.Read(b)
	var s strings.Builder
	for i, c := range b {
		if i > 0 && i%5 == 0 {
			s.WriteByte('-')
		}
		s.WriteByte(alpha[int(c)%len(alpha)])
	}
	return s.String()
}

// sealKeys encrypts the bundle with the passphrase.
func sealKeys(k *offsiteKeys, pass string) ([]byte, error) {
	r, err := age.NewScryptRecipient(pass)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(k)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// errPassphrase: the passphrase does not open the destination's key.
var errPassphrase = errors.New("the passphrase does not match the off-box copies at this destination")

func openKeys(raw []byte, pass string) (*offsiteKeys, error) {
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		var nm *age.NoIdentityMatchError
		if errors.As(err, &nm) || strings.Contains(err.Error(), "incorrect passphrase") {
			return nil, errPassphrase
		}
		return nil, fmt.Errorf("reading the key bundle: %w", err)
	}
	var k offsiteKeys
	if err := json.NewDecoder(r).Decode(&k); err != nil {
		return nil, fmt.Errorf("reading the key bundle: %w", err)
	}
	if k.Version != vaultVersion || len(k.MAC) != 32 {
		return nil, fmt.Errorf("the key bundle is version %d; this Tiffin reads version %d", k.Version, vaultVersion)
	}
	return &k, nil
}

// vault reads and writes one destination's sets and chunks.
type vault struct {
	st     objectStore
	prefix string // ends in "tiffin/"
	id     *age.X25519Identity
	mac    []byte
}

func vaultPrefix(prefix string) string {
	p := strings.Trim(prefix, "/")
	if p == "" {
		return "tiffin/"
	}
	return p + "/tiffin/"
}

func newVault(st objectStore, prefix string, k *offsiteKeys) (*vault, error) {
	id, err := age.ParseX25519Identity(k.Identity)
	if err != nil {
		return nil, err
	}
	return &vault{st: st, prefix: vaultPrefix(prefix), id: id, mac: k.MAC}, nil
}

var (
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	zdec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
)

// seal compresses and encrypts.
func (v *vault) seal(plain []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, v.id.Recipient())
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(zenc.EncodeAll(plain, nil)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (v *vault) open(ct []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ct), v.id)
	if err != nil {
		return nil, err
	}
	z, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return zdec.DecodeAll(z, nil)
}

func (v *vault) chunkID(plain []byte) string {
	m := hmac.New(sha256.New, v.mac)
	m.Write(plain)
	return hex.EncodeToString(m.Sum(nil))
}

func (v *vault) chunkKey(id string) string { return v.prefix + "chunks/" + id[:2] + "/" + id }
func (v *vault) keyKey() string            { return v.prefix + "key" }
func (v *vault) setKey(id, part string) string {
	return v.prefix + "sets/" + id + "/" + part
}

func (v *vault) putJSON(ctx context.Context, key string, x any) error {
	raw, err := json.Marshal(x)
	if err != nil {
		return err
	}
	ct, err := v.seal(raw)
	if err != nil {
		return err
	}
	return v.st.Put(ctx, key, ct)
}

func (v *vault) getJSON(ctx context.Context, key string, x any) error {
	ct, err := v.st.Get(ctx, key)
	if err != nil {
		return err
	}
	raw, err := v.open(ct)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return json.Unmarshal(raw, x)
}

// ---- trees ----

// treeEntry is one directory, file or symlink of a set.
type treeEntry struct {
	Path   string            `json:"p"`
	Type   byte              `json:"t"` // tar type flag
	Mode   int64             `json:"m"`
	UID    int               `json:"u"`
	GID    int               `json:"g"`
	User   string            `json:"un,omitempty"`
	Group  string            `json:"gn,omitempty"`
	MTime  int64             `json:"mt"` // unix nanoseconds
	Size   int64             `json:"s,omitempty"`
	Link   string            `json:"l,omitempty"`
	Xattrs map[string]string `json:"x,omitempty"` // SCHILY.xattr.* PAX records
	Chunks []string          `json:"c,omitempty"`
}

func (e *treeEntry) header() *tar.Header {
	return &tar.Header{Name: e.Path, Typeflag: e.Type, Mode: e.Mode, Uid: e.UID, Gid: e.GID, Uname: e.User, Gname: e.Group,
		ModTime: time.Unix(0, e.MTime), Size: e.Size, Linkname: e.Link, PAXRecords: e.Xattrs, Format: tar.FormatPAX}
}

// fileMemo is what the previous upload learnt about one file.
type fileMemo struct {
	Size   int64    `json:"s"`
	MTime  int64    `json:"mt"`
	Chunks []string `json:"c"`
}

// uploadStats says what an upload did.
type uploadStats struct {
	Files      int64 `json:"files"`
	Bytes      int64 `json:"bytes" doc:"Size of the files in the set"`
	Chunks     int   `json:"chunks"`
	NewChunks  int   `json:"newChunks" doc:"Chunks the destination did not have yet"`
	SentBytes  int64 `json:"sentBytes" doc:"Bytes uploaded (compressed and encrypted)"`
	ReusedRead int64 `json:"reusedFiles" doc:"Files unchanged since the last upload (not read again)"`
}

// putTree uploads the files under dir: chunks the destination lacks (known
// says which it has; it gains the new ones) and returns the tree. memo maps
// a path to what the last upload found; it is updated.
func (v *vault) putTree(ctx context.Context, dir string, known map[string]bool, memo map[string]fileMemo) ([]treeEntry, uploadStats, error) {
	var st uploadStats
	var entries []treeEntry
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type job struct {
		id    string
		plain []byte
	}
	jobs := make(chan job, putWorkers)
	var (
		mu      sync.Mutex
		firstEr error
		wg      sync.WaitGroup
		pending = map[string]bool{}
	)
	fail := func(err error) {
		mu.Lock()
		if firstEr == nil {
			firstEr = err
			cancel()
		}
		mu.Unlock()
	}
	for range putWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				ct, err := v.seal(j.plain)
				if err == nil {
					err = v.st.Put(ctx, v.chunkKey(j.id), ct)
				}
				if err != nil {
					fail(fmt.Errorf("upload chunk: %w", err))
					continue
				}
				mu.Lock()
				known[j.id] = true
				st.NewChunks++
				st.SentBytes += int64(len(ct))
				mu.Unlock()
			}
		}()
	}
	send := func(id string, plain []byte) error {
		mu.Lock()
		skip := known[id] || pending[id]
		pending[id] = true
		mu.Unlock()
		if skip {
			return nil
		}
		select {
		case jobs <- job{id, plain}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	walkErr := boxfile.Walk(dir, boxfile.TreeOptions{}, func(rel string, hd *tar.Header, src string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := treeEntry{Path: rel, Type: hd.Typeflag, Mode: hd.Mode, UID: hd.Uid, GID: hd.Gid, User: hd.Uname, Group: hd.Gname,
			MTime: hd.ModTime.UnixNano(), Link: hd.Linkname, Xattrs: hd.PAXRecords}
		if hd.Typeflag == tar.TypeReg {
			e.Size = hd.Size
			st.Files++
			st.Bytes += hd.Size
			m, ok := memo[rel]
			mu.Lock()
			reuse := ok && m.Size == hd.Size && m.MTime == e.MTime && allKnown(known, m.Chunks)
			mu.Unlock()
			if reuse {
				e.Chunks = m.Chunks
				st.ReusedRead++
			} else {
				ids, err := v.chunkFile(src, hd.Size, send)
				if err != nil {
					return fmt.Errorf("%s: %w", rel, err)
				}
				e.Chunks = ids
				memo[rel] = fileMemo{Size: hd.Size, MTime: e.MTime, Chunks: ids}
			}
			st.Chunks += len(e.Chunks)
		}
		entries = append(entries, e)
		return nil
	})
	close(jobs)
	wg.Wait()
	if firstEr != nil {
		return nil, st, firstEr
	}
	if walkErr != nil {
		return nil, st, walkErr
	}
	return entries, st, nil
}

func allKnown(known map[string]bool, ids []string) bool {
	for _, id := range ids {
		if !known[id] {
			return false
		}
	}
	return true
}

// chunkFile reads a file in chunks and hands each to send.
func (v *vault) chunkFile(src string, size int64, send func(id string, plain []byte) error) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ids := []string{}
	var n int64
	for {
		buf := make([]byte, chunkSize)
		k, err := io.ReadFull(f, buf)
		if k > 0 {
			id := v.chunkID(buf[:k])
			ids = append(ids, id)
			n += int64(k)
			if err := send(id, buf[:k]); err != nil {
				return nil, err
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if n != size {
		return nil, fmt.Errorf("changed while it was read (%d bytes, expected %d)", n, size)
	}
	return ids, nil
}

// getStats says what a download did.
type getStats struct {
	Files  int64 `json:"files"`
	Bytes  int64 `json:"bytes"`
	Chunks int   `json:"chunks"`
}

// getTree writes a tree below dest (which should be empty), fetching chunks
// in parallel and checking each against its ID. only, when set, limits it
// to entries under those top-level paths.
func (v *vault) getTree(ctx context.Context, entries []treeEntry, dest string, only ...string) (getStats, error) {
	var st getStats
	keep := func(p string) bool {
		if len(only) == 0 {
			return true
		}
		for _, o := range only {
			if p == "" || p == o || strings.HasPrefix(p, o+"/") || strings.HasPrefix(o, p+"/") {
				return true
			}
		}
		return false
	}
	ctx, cancel := context.WithCancel(ctx)
	// On return, stop and wait for every fetch still running, so none
	// outlives the call (a failed download returns early).
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	// Fetch every chunk in order, getWorkers at a time; the writer below
	// takes them in the same order.
	queue := make(chan chan fetched, getWorkers)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(queue)
		sem := make(chan struct{}, getWorkers)
		for i := range entries {
			if !keep(entries[i].Path) {
				continue
			}
			for _, id := range entries[i].Chunks {
				ch := make(chan fetched, 1)
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				select {
				case queue <- ch:
				case <-ctx.Done():
					return
				}
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					defer func() { <-sem }()
					ch <- v.fetchChunk(ctx, id)
				}(id)
			}
		}
	}()
	x, err := boxfile.NewExtractor(dest)
	if err != nil {
		return st, err
	}
	next := func() ([]byte, error) {
		select {
		case ch, ok := <-queue:
			if !ok {
				return nil, errors.New("tree ended early")
			}
			r := <-ch
			return r.plain, r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	for i := range entries {
		e := &entries[i]
		if !keep(e.Path) {
			continue
		}
		var body io.Reader
		if e.Type == tar.TypeReg {
			body = &chunkReader{n: len(e.Chunks), next: next}
			st.Files++
			st.Bytes += e.Size
			st.Chunks += len(e.Chunks)
		}
		if err := x.Add(e.Path, e.header(), body); err != nil {
			x.Close()
			return st, fmt.Errorf("%s: %w", e.Path, err)
		}
	}
	return st, x.Close()
}

// fetched is a chunk read back from the destination.
type fetched struct {
	plain []byte
	err   error
}

// fetchChunk downloads, decrypts and checks one chunk.
func (v *vault) fetchChunk(ctx context.Context, id string) fetched {
	ct, err := v.st.Get(ctx, v.chunkKey(id))
	if errors.Is(err, errNoObject) {
		return fetched{err: fmt.Errorf("chunk %s is missing from the destination", id[:12])}
	}
	if err != nil {
		return fetched{err: err}
	}
	plain, err := v.open(ct)
	if err != nil {
		return fetched{err: fmt.Errorf("chunk %s cannot be decrypted: %w", id[:12], err)}
	}
	if v.chunkID(plain) != id {
		return fetched{err: fmt.Errorf("chunk %s is damaged (its content does not match its ID)", id[:12])}
	}
	return fetched{plain: plain}
}

// chunkReader reads n chunks in order from next.
type chunkReader struct {
	n    int
	next func() ([]byte, error)
	buf  []byte
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if c.n == 0 {
			return 0, io.EOF
		}
		b, err := c.next()
		if err != nil {
			return 0, err
		}
		c.n--
		c.buf = b
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

// ---- sets ----

// offsiteSet is a set's record in the destination (sets/<id>/info).
type offsiteSet struct {
	Backup   Backup      `json:"backup"`
	Box      string      `json:"box"`
	Version  string      `json:"tiffinVersion"`
	CopiedAt time.Time   `json:"copiedAt"`
	Upload   uploadStats `json:"upload"`
}

// putSet uploads the set directory dir and then its tree and record; a set
// counts as copied once its record exists.
func (v *vault) putSet(ctx context.Context, rec *offsiteSet, dir string, known map[string]bool, memo map[string]fileMemo) ([]treeEntry, error) {
	entries, st, err := v.putTree(ctx, dir, known, memo)
	if err != nil {
		return nil, err
	}
	rec.Upload = st
	if err := v.putJSON(ctx, v.setKey(rec.Backup.ID, "tree"), entries); err != nil {
		return nil, err
	}
	rec.CopiedAt = time.Now().UTC()
	if err := v.putJSON(ctx, v.setKey(rec.Backup.ID, "info"), rec); err != nil {
		return nil, err
	}
	return entries, nil
}

// setIDs lists the IDs of the sets with a record, oldest first.
func (v *vault) setIDs(ctx context.Context) ([]string, error) {
	var ids []string
	err := v.st.List(ctx, v.prefix+"sets/", func(key string, _ int64) error {
		rest := strings.TrimPrefix(key, v.prefix+"sets/")
		if id, part, ok := strings.Cut(rest, "/"); ok && part == "info" {
			ids = append(ids, id)
		}
		return nil
	})
	sort.Strings(ids)
	return ids, err
}

func (v *vault) getSet(ctx context.Context, id string) (*offsiteSet, error) {
	var s offsiteSet
	if err := v.getJSON(ctx, v.setKey(id, "info"), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (v *vault) getEntries(ctx context.Context, id string) ([]treeEntry, error) {
	var e []treeEntry
	err := v.getJSON(ctx, v.setKey(id, "tree"), &e)
	return e, err
}

func (v *vault) deleteSet(ctx context.Context, id string) error {
	// The record first: without it the set is gone, whatever is left.
	if err := v.st.Delete(ctx, v.setKey(id, "info")); err != nil {
		return err
	}
	return v.st.Delete(ctx, v.setKey(id, "tree"))
}

// chunkIDs lists every chunk in the destination.
func (v *vault) chunkIDs(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	err := v.st.List(ctx, v.prefix+"chunks/", func(key string, _ int64) error {
		out[path.Base(key)] = true
		return nil
	})
	return out, err
}

// pruneResult says what a prune removed.
type pruneResult struct {
	Sets   []string `json:"sets"`
	Chunks int      `json:"chunks"`
}

// prune deletes the sets drop says to (never the newest one) and then
// every chunk no remaining set uses. refs returns a set's chunk IDs.
func (v *vault) prune(ctx context.Context, drop func(id string) bool, refs func(id string) ([]string, error)) (pruneResult, error) {
	var res pruneResult
	ids, err := v.setIDs(ctx)
	if err != nil {
		return res, err
	}
	var keep []string
	for i, id := range ids {
		if i < len(ids)-1 && drop(id) {
			if err := v.deleteSet(ctx, id); err != nil {
				return res, err
			}
			res.Sets = append(res.Sets, id)
			continue
		}
		keep = append(keep, id)
	}
	used := map[string]bool{}
	for _, id := range keep {
		r, err := refs(id)
		if err != nil {
			return res, fmt.Errorf("set %s: %w", id, err) // never delete chunks on a partial view
		}
		for _, c := range r {
			used[c] = true
		}
	}
	all, err := v.chunkIDs(ctx)
	if err != nil {
		return res, err
	}
	for c := range all {
		if !used[c] {
			if err := v.st.Delete(ctx, v.chunkKey(c)); err != nil {
				return res, err
			}
			res.Chunks++
		}
	}
	return res, nil
}

// treeChunks is every chunk ID a tree uses.
func treeChunks(entries []treeEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Chunks...)
	}
	return out
}

// ulidTime is when an ID made by ids.New was made.
func ulidTime(id string) (time.Time, bool) {
	_, body, ok := strings.Cut(id, "_")
	if !ok || len(body) != 26 {
		return time.Time{}, false
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var ms uint64
	for _, c := range body[:10] {
		i := strings.IndexRune(alphabet, c)
		if i < 0 {
			return time.Time{}, false
		}
		ms = ms<<5 | uint64(i)
	}
	return time.UnixMilli(int64(ms)).UTC(), true
}
