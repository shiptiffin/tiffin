// Package boxfile reads and writes Tiffin export archives (.tiffin, of a
// whole box or of one project): the format, its verification and safe
// extraction. The portable module (internal/mod/portable) fills them; the
// CLI reads their manifest.
package boxfile

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// The archive is a zstd-compressed tar stream:
//
//	tiffin-export.json        the Manifest (always the first entry)
//	...                       the box's parts (see export.go for the order)
//	tiffin-export-end.json    the Trailer (always the last entry)
//
// Streams whose size is not known up front (pg_dump, nerdctl save) are split
// into numbered chunk entries ("name@000000", "name@000001", ...) so nothing
// has to be spooled to disk. Every entry's name, type, size, link target and
// content feed one running SHA-256 (the trailer's Digest), so a truncated,
// reordered or altered archive is caught on import; zstd frames carry their
// own checksums too.
const (
	ManifestName = "tiffin-export.json"
	TrailerName  = "tiffin-export-end.json"
	// ChunkSize is the size of one chunk of a stream entry.
	ChunkSize = 16 << 20
)

// chunkSize is ChunkSize (tests shrink it).
var chunkSize = ChunkSize

// ErrCorrupt means the archive failed verification.
var ErrCorrupt = errors.New("the archive is damaged or incomplete")

// Trailer closes an archive.
type Trailer struct {
	Entries int              `json:"entries" doc:"Tar entries before the trailer (chunks count one each)"`
	Bytes   int64            `json:"bytes" doc:"Uncompressed content bytes"`
	Digest  string           `json:"digest" doc:"SHA-256 over every entry's name, type, size, link and content, in order"`
	Parts   map[string]Stats `json:"parts" doc:"What each part holds"`
}

// Stats sums up one part of the archive.
type Stats struct {
	Files int   `json:"files,omitempty"`
	Bytes int64 `json:"bytes"`
	Items int   `json:"items,omitempty" doc:"Part-specific count: databases, keys, images, ..."`
}

// entryDigest feeds one entry's header fields into the running digest.
func entryDigest(h hash.Hash, hd *tar.Header) {
	fmt.Fprintf(h, "%s\x00%c\x00%d\x00%s\x00", hd.Name, hd.Typeflag, hd.Size, hd.Linkname)
}

// ---- writing ----

// Writer writes an archive.
type Writer struct {
	cw     *countingWriter // compressed bytes out (size and sha256 of the file)
	zw     *zstd.Encoder
	tw     *tar.Writer
	digest hash.Hash
	tr     Trailer
	// Progress, if set, is called after content bytes are written.
	Progress func(n int64)
	owners   *ownerCache
	closed   bool
}

type countingWriter struct {
	w   io.Writer
	n   int64
	sha hash.Hash
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.sha.Write(p[:n])
	return n, err
}

// NewWriter starts an archive on w and writes the manifest.
func NewWriter(w io.Writer, m *Manifest) (*Writer, error) {
	cw := &countingWriter{w: w, sha: sha256.New()}
	zw, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(2))
	if err != nil {
		return nil, err
	}
	aw := &Writer{cw: cw, zw: zw, tw: tar.NewWriter(zw), digest: sha256.New(), owners: newOwnerCache()}
	aw.tr.Parts = map[string]Stats{}
	if err := aw.WriteJSON(ManifestName, m); err != nil {
		return nil, err
	}
	return aw, nil
}

// header writes a tar header and feeds the digest.
func (w *Writer) header(hd *tar.Header) error {
	if err := w.tw.WriteHeader(hd); err != nil {
		return err
	}
	entryDigest(w.digest, hd)
	w.tr.Entries++
	return nil
}

func (w *Writer) content(r io.Reader, n int64) error {
	got, err := io.CopyN(io.MultiWriter(w.tw, w.digest), r, n)
	w.tr.Bytes += got
	if w.Progress != nil {
		w.Progress(got)
	}
	if err != nil {
		return err
	}
	return nil
}

// WriteBytes writes one regular file entry.
func (w *Writer) WriteBytes(name string, b []byte, mode int64) error {
	if err := w.header(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(b)), Mode: mode, ModTime: time.Now().UTC().Truncate(time.Second), Format: tar.FormatPAX}); err != nil {
		return err
	}
	return w.content(bytes.NewReader(b), int64(len(b)))
}

// WriteJSON writes v as an indented JSON entry.
func (w *Writer) WriteJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return w.WriteBytes(name, append(b, '\n'), 0o600)
}

// WriteFile writes the file at src as one entry.
func (w *Writer) WriteFile(name, src string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	hd := &tar.Header{Name: name, Typeflag: tar.TypeReg, Size: fi.Size(), Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime(), Format: tar.FormatPAX}
	if err := w.header(hd); err != nil {
		return 0, err
	}
	return fi.Size(), w.contentPadded(f, fi.Size())
}

// contentPadded copies exactly n bytes, padding with zeros if the file
// shrank while it was being read (only possible when copying live files).
func (w *Writer) contentPadded(r io.Reader, n int64) error {
	got, err := io.CopyN(io.MultiWriter(w.tw, w.digest), r, n)
	w.tr.Bytes += got
	if w.Progress != nil {
		w.Progress(got)
	}
	if errors.Is(err, io.EOF) && got < n {
		return w.content(zeros{}, n-got)
	}
	return err
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// WriteStream writes r, whose size is not known up front, as numbered chunk
// entries "name@000000", ... It returns the bytes written.
func (w *Writer) WriteStream(name string, r io.Reader) (int64, error) {
	buf := make([]byte, chunkSize)
	var total int64
	for i := 0; ; i++ {
		n, err := io.ReadFull(r, buf)
		if n > 0 || i == 0 {
			hd := &tar.Header{Name: fmt.Sprintf("%s@%06d", name, i), Typeflag: tar.TypeReg, Size: int64(n), Mode: 0o600, ModTime: time.Now().UTC().Truncate(time.Second), Format: tar.FormatPAX,
				PAXRecords: map[string]string{paxChunk: strconv.Itoa(i)}}
			if herr := w.header(hd); herr != nil {
				return total, herr
			}
			if cerr := w.content(bytes.NewReader(buf[:n]), int64(n)); cerr != nil {
				return total, cerr
			}
			total += int64(n)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// TreeOptions control WriteTree.
type TreeOptions struct {
	// Skip leaves out a path (relative, slash-separated; directories are
	// skipped with everything under them).
	Skip func(rel string, d fs.DirEntry) bool
	// Replace takes a regular file's content from another file (relative
	// path → source), e.g. a consistent snapshot of a live SQLite database.
	Replace map[string]string
}

// WriteTree writes the directory root under prefix: directories, regular
// files, symlinks, modes, owners (by name), times and user.* extended
// attributes (object stores keep metadata there). Sockets, devices and FIFOs
// are left out. A missing root writes nothing.
func (w *Writer) WriteTree(prefix, root string, o TreeOptions) (Stats, error) {
	var st Stats
	err := walk(root, o, w.owners, func(rel string, hd *tar.Header, src string) error {
		name := prefix
		if rel != "" {
			name = prefix + "/" + rel
		}
		if hd.Typeflag == tar.TypeDir {
			name += "/"
		}
		hd.Name = name
		if hd.Typeflag != tar.TypeReg {
			return w.header(hd)
		}
		f, err := os.Open(src)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		defer f.Close()
		if err := w.header(hd); err != nil {
			return err
		}
		st.Files++
		st.Bytes += hd.Size
		return w.contentPadded(f, hd.Size)
	})
	return st, err
}

// Walk calls fn for root and every directory, regular file and symlink
// under it (root first, with rel ""), with the header WriteTree writes for
// it (Name empty): mode, owners by name, modification time and user.*
// extended attributes as SCHILY.xattr.* PAX records. src is where a regular
// file's content is read from (o.Replace applied). A missing root walks
// nothing.
func Walk(root string, o TreeOptions, fn func(rel string, hd *tar.Header, src string) error) error {
	return walk(root, o, newOwnerCache(), fn)
}

func walk(root string, o TreeOptions, owners *ownerCache, fn func(rel string, hd *tar.Header, src string) error) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // removed while walking (live copy)
			}
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel != "." && o.Skip != nil && o.Skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		link := ""
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case fi.IsDir(), fi.Mode().IsRegular():
		default:
			return nil // sockets, devices, pipes
		}
		hd, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		hd.Format = tar.FormatPAX
		hd.Name = ""
		hd.Uname, hd.Gname = owners.names(hd.Uid, hd.Gid)
		hd.AccessTime, hd.ChangeTime = time.Time{}, time.Time{}
		if fi.Mode()&os.ModeSymlink == 0 {
			if xa, err := listXattrs(p); err == nil && len(xa) > 0 {
				hd.PAXRecords = map[string]string{}
				for k, v := range xa {
					hd.PAXRecords["SCHILY.xattr."+k] = v
				}
			}
		}
		src := p
		if alt, ok := o.Replace[rel]; ok && fi.Mode().IsRegular() {
			afi, err := os.Stat(alt)
			if err != nil {
				return err
			}
			src, hd.Size = alt, afi.Size()
		}
		if rel == "." {
			rel = ""
		}
		return fn(rel, hd, src)
	})
}

// Written is the number of archive (compressed) bytes written so far.
func (w *Writer) Written() int64 { return w.cw.n }

// Part records what a part holds, for the trailer.
func (w *Writer) Part(name string, s Stats) { w.tr.Parts[name] = s }

// Close writes the trailer and finishes the stream. It returns the archive's
// compressed size and SHA-256.
func (w *Writer) Close() (int64, string, *Trailer, error) {
	if w.closed {
		return w.cw.n, hex.EncodeToString(w.cw.sha.Sum(nil)), &w.tr, nil
	}
	w.closed = true
	w.tr.Digest = hex.EncodeToString(w.digest.Sum(nil))
	tr := w.tr
	b, _ := json.MarshalIndent(&tr, "", "  ")
	// The trailer itself is outside the digest.
	if err := w.tw.WriteHeader(&tar.Header{Name: TrailerName, Typeflag: tar.TypeReg, Size: int64(len(b)), Mode: 0o600, ModTime: time.Now().UTC().Truncate(time.Second), Format: tar.FormatPAX}); err != nil {
		return 0, "", nil, err
	}
	if _, err := w.tw.Write(b); err != nil {
		return 0, "", nil, err
	}
	if err := w.tw.Close(); err != nil {
		return 0, "", nil, err
	}
	if err := w.zw.Close(); err != nil {
		return 0, "", nil, err
	}
	return w.cw.n, hex.EncodeToString(w.cw.sha.Sum(nil)), &tr, nil
}

// Abort releases the compressor without finishing the archive.
func (w *Writer) Abort() {
	if !w.closed {
		w.closed = true
		w.zw.Close()
	}
}

// ---- reading ----

// Reader reads an archive entry by entry.
type Reader struct {
	Manifest Manifest
	Trailer  *Trailer // set once Next has returned io.EOF

	zr      *zstd.Decoder
	tr      *tar.Reader
	digest  hash.Hash
	entries int
	bytes   int64
	cur     io.Reader // unread content of the current entry
	peek    *tar.Header
}

// NewReader opens an archive and reads its manifest. It refuses archives
// that are not Tiffin box exports.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	magic, _ := br.Peek(4)
	if !bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return nil, errors.New("not a Tiffin export (it is not zstd-compressed)")
	}
	zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(2), zstd.WithDecoderMaxWindow(64<<20))
	if err != nil {
		return nil, err
	}
	ar := &Reader{zr: zr, tr: tar.NewReader(zr), digest: sha256.New()}
	hd, err := ar.tr.Next()
	if err != nil {
		zr.Close()
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if hd.Name != ManifestName || hd.Size > 16<<20 {
		zr.Close()
		return nil, fmt.Errorf("not a Tiffin export: first entry is %q, want %s", hd.Name, ManifestName)
	}
	entryDigest(ar.digest, hd)
	ar.entries++
	raw, err := io.ReadAll(io.TeeReader(ar.tr, ar.digest))
	if err != nil {
		zr.Close()
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	ar.bytes += int64(len(raw))
	if err := json.Unmarshal(raw, &ar.Manifest); err != nil {
		zr.Close()
		return nil, fmt.Errorf("not a Tiffin export: manifest: %w", err)
	}
	if ar.Manifest.Kind != ManifestKind && ar.Manifest.Kind != ProjectKind {
		zr.Close()
		return nil, fmt.Errorf("not a Tiffin export (kind %q)", ar.Manifest.Kind)
	}
	return ar, nil
}

// Close releases the decompressor.
func (r *Reader) Close() { r.zr.Close() }

// Entry is one logical entry: a file, directory or symlink, or a whole
// chunked stream (Stream true; Body reads across its chunks).
type Entry struct {
	Name   string
	Header *tar.Header // the (first chunk's) header
	Stream bool
	Body   io.Reader
}

// paxChunk marks chunk entries (names alone could clash with object keys).
const paxChunk = "TIFFIN.chunk"

// chunkOf reports whether hd is a stream chunk, its stream and number.
func chunkOf(hd *tar.Header) (base string, n int, ok bool) {
	if _, marked := hd.PAXRecords[paxChunk]; !marked {
		return "", 0, false
	}
	return chunkName(hd.Name)
}

func chunkName(name string) (base string, n int, ok bool) {
	i := strings.LastIndexByte(name, '@')
	if i < 0 || len(name)-i != 7 {
		return "", 0, false
	}
	n, err := strconv.Atoi(name[i+1:])
	if err != nil {
		return "", 0, false
	}
	return name[:i], n, true
}

func (r *Reader) nextHeader() (*tar.Header, error) {
	// Whatever the caller left unread still counts (and a chunk stream
	// finds the header after its last chunk while draining).
	if r.cur != nil {
		if _, err := io.Copy(io.Discard, r.cur); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		r.cur = nil
	}
	if r.peek != nil {
		hd := r.peek
		r.peek = nil
		return hd, nil
	}
	hd, err := r.tr.Next()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: it ends before its trailer (truncated?)", ErrCorrupt)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return hd, nil
}

// open makes hd's content the current reader, feeding the digest.
func (r *Reader) open(hd *tar.Header) io.Reader {
	entryDigest(r.digest, hd)
	r.entries++
	r.cur = &countReader{r: io.TeeReader(r.tr, r.digest), n: &r.bytes}
	return r.cur
}

type countReader struct {
	r io.Reader
	n *int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// Next returns the next logical entry, or io.EOF after the trailer has been
// read and verified. A verification failure is an ErrCorrupt.
func (r *Reader) Next() (*Entry, error) {
	hd, err := r.nextHeader()
	if err != nil {
		return nil, err
	}
	if hd.Name == TrailerName {
		if r.cur != nil {
			_, _ = io.Copy(io.Discard, r.cur)
			r.cur = nil
		}
		raw, err := io.ReadAll(io.LimitReader(r.tr, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("%w: trailer: %v", ErrCorrupt, err)
		}
		var t Trailer
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("%w: trailer: %v", ErrCorrupt, err)
		}
		got := hex.EncodeToString(r.digest.Sum(nil))
		if t.Digest != got || t.Entries != r.entries || t.Bytes != r.bytes {
			return nil, fmt.Errorf("%w: content digest or counts differ from the trailer (%d/%d entries, %d/%d bytes)", ErrCorrupt, r.entries, t.Entries, r.bytes, t.Bytes)
		}
		if _, err := r.tr.Next(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: data after the trailer", ErrCorrupt)
		}
		r.Trailer = &t
		return nil, io.EOF
	}
	if err := safeName(hd.Name); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	base, n, ok := chunkOf(hd)
	if !ok {
		return &Entry{Name: hd.Name, Header: hd, Body: r.open(hd)}, nil
	}
	if n != 0 {
		return nil, fmt.Errorf("%w: chunk %s without its start", ErrCorrupt, hd.Name)
	}
	cs := &chunkStream{r: r, base: base, next: 1}
	cs.cur = r.open(hd)
	r.cur = cs
	return &Entry{Name: base, Header: hd, Stream: true, Body: cs}, nil
}

// chunkStream reads one stream across its chunk entries.
type chunkStream struct {
	r    *Reader
	base string
	next int
	cur  io.Reader
	done bool
	err  error
}

func (c *chunkStream) Read(p []byte) (int, error) {
	for {
		if c.err != nil {
			return 0, c.err
		}
		if c.done {
			return 0, io.EOF
		}
		n, err := c.cur.Read(p)
		if n > 0 {
			return n, nil
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			c.err = fmt.Errorf("%w: %v", ErrCorrupt, err)
			return 0, c.err
		}
		// This chunk is done: is the next entry our next chunk?
		hd, err := c.r.tr.Next()
		if err != nil {
			c.err = fmt.Errorf("%w: %s: %v", ErrCorrupt, c.base, err)
			return 0, c.err
		}
		base, n2, ok := chunkOf(hd)
		if !ok || base != c.base {
			c.r.peek = hd
			c.done = true
			return 0, io.EOF
		}
		if n2 != c.next {
			c.err = fmt.Errorf("%w: %s: chunk %d where %d was expected", ErrCorrupt, c.base, n2, c.next)
			return 0, c.err
		}
		c.next++
		c.cur = &countReader{r: io.TeeReader(c.r.tr, c.r.digest), n: &c.r.bytes}
		entryDigest(c.r.digest, hd)
		c.r.entries++
	}
}

// safeName refuses absolute names and names that climb out with "..".
func safeName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("unsafe entry name %q", name)
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == ".." || part == "" {
			return fmt.Errorf("unsafe entry name %q", name)
		}
	}
	return nil
}

// Verify reads a whole archive and checks it against its trailer.
func Verify(r io.Reader) (*Manifest, *Trailer, error) {
	ar, err := NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	defer ar.Close()
	for {
		_, err := ar.Next()
		if errors.Is(err, io.EOF) {
			return &ar.Manifest, ar.Trailer, nil
		}
		if err != nil {
			return &ar.Manifest, nil, err
		}
	}
}

// ---- extracting trees ----

// Extractor writes tree entries below a destination directory. It never
// writes outside it (os.Root), restores owners by name when running as
// root, and sets directory times once everything is written.
type Extractor struct {
	root   *os.Root
	dir    string
	chown  bool
	owners *ownerCache
	dirs   map[string]time.Time
	Files  int
	Bytes  int64
}

// NewExtractor prepares dest (created if missing; it should be empty).
func NewExtractor(dest string) (*Extractor, error) {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return nil, err
	}
	return &Extractor{root: root, dir: dest, chown: os.Geteuid() == 0, owners: newOwnerCache(), dirs: map[string]time.Time{}}, nil
}

// Add writes one entry; rel is its path below the destination ("" for the
// destination itself).
func (x *Extractor) Add(rel string, hd *tar.Header, body io.Reader) error {
	name := strings.TrimSuffix(rel, "/")
	if name == "" {
		name = "."
	}
	if dir := path.Dir(name); dir != "." && name != "." {
		if err := x.root.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	mode := hd.FileInfo().Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	switch hd.Typeflag {
	case tar.TypeDir:
		if name != "." {
			if err := x.root.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		}
		if f, err := x.root.Open(name); err == nil {
			_ = setXattrs(f, hd.PAXRecords)
			f.Close()
		}
		x.dirs[name] = hd.ModTime
	case tar.TypeSymlink:
		if err := x.root.Symlink(hd.Linkname, name); err != nil {
			return err
		}
		return x.lchown(name, hd)
	case tar.TypeReg:
		f, err := x.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, body)
		if err == nil && n != hd.Size {
			err = fmt.Errorf("%s: wrote %d of %d bytes", name, n, hd.Size)
		}
		if err == nil {
			err = setXattrs(f, hd.PAXRecords)
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		x.Files++
		x.Bytes += n
	default:
		return nil // not written by WriteTree
	}
	// Owner first: chown clears set-id bits, chmod puts them back.
	if err := x.lchown(name, hd); err != nil {
		return err
	}
	if err := x.root.Chmod(name, mode); err != nil {
		return err
	}
	if hd.Typeflag == tar.TypeReg {
		return x.root.Chtimes(name, hd.ModTime, hd.ModTime)
	}
	return nil
}

func (x *Extractor) lchown(name string, hd *tar.Header) error {
	if !x.chown {
		return nil
	}
	uid, gid := x.owners.ids(hd.Uname, hd.Gname, hd.Uid, hd.Gid)
	return x.root.Lchown(name, uid, gid)
}

// Close sets directory times (deepest first) and releases the root.
func (x *Extractor) Close() error {
	names := make([]string, 0, len(x.dirs))
	for n := range x.dirs {
		names = append(names, n)
	}
	// Longer paths first, so setting a child's time does not bump its parent's.
	slices.SortFunc(names, func(a, b string) int { return len(b) - len(a) })
	for _, n := range names {
		_ = x.root.Chtimes(n, x.dirs[n], x.dirs[n])
	}
	return x.root.Close()
}

// ---- owners ----

// ownerCache maps uids/gids to names and back, so files keep their owner
// by name on a box where the service users have different ids.
type ownerCache struct {
	mu     sync.Mutex
	unames map[int]string
	gnames map[int]string
	uids   map[string]int
	gids   map[string]int
}

func newOwnerCache() *ownerCache {
	return &ownerCache{unames: map[int]string{}, gnames: map[int]string{}, uids: map[string]int{}, gids: map[string]int{}}
}

func (c *ownerCache) names(uid, gid int) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	un, ok := c.unames[uid]
	if !ok {
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
			un = u.Username
		}
		c.unames[uid] = un
	}
	gn, ok := c.gnames[gid]
	if !ok {
		if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
			gn = g.Name
		}
		c.gnames[gid] = gn
	}
	return un, gn
}

func (c *ownerCache) ids(uname, gname string, uid, gid int) (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if uname != "" {
		v, ok := c.uids[uname]
		if !ok {
			v = -1
			if u, err := user.Lookup(uname); err == nil {
				v, _ = strconv.Atoi(u.Uid)
			}
			c.uids[uname] = v
		}
		if v >= 0 {
			uid = v
		}
	}
	if gname != "" {
		v, ok := c.gids[gname]
		if !ok {
			v = -1
			if g, err := user.LookupGroup(gname); err == nil {
				v, _ = strconv.Atoi(g.Gid)
			}
			c.gids[gname] = v
		}
		if v >= 0 {
			gid = v
		}
	}
	return uid, gid
}
