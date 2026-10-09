package storage

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// Usage is the measured size of one bucket.
type Usage struct {
	Bytes   int64 `json:"bytes"`
	Objects int64 `json:"objects"`
}

// usageTracker keeps per-bucket usage from periodic scans of the data dir,
// plus what writes through the front server changed since the last scan,
// plus what uploads in progress have reserved, so a burst of uploads (in
// a row or at once) cannot run past a quota between scans.
type usageTracker struct {
	mu         sync.Mutex
	buckets    map[string]Usage // by S3 name
	pending    map[string]int64 // by S3 name: bytes added (or freed) since the last scan
	inflight   map[string]int64 // by S3 name: bytes reserved by uploads still running
	measuredAt time.Time
	wake       chan struct{}
}

func newUsageTracker() *usageTracker {
	return &usageTracker{buckets: map[string]Usage{}, pending: map[string]int64{}, inflight: map[string]int64{}, wake: make(chan struct{}, 1)}
}

// scanDir measures every bucket directory under dir. Multipart parts in
// flight (.sgwtmp) count toward bytes but not objects.
func scanDir(dir string) (map[string]Usage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]Usage{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out[e.Name()] = scanBucket(filepath.Join(dir, e.Name()))
	}
	return out, nil
}

func scanBucket(dir string) Usage {
	var u Usage
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		u.Bytes += info.Size()
		if !strings.Contains(path[len(dir):], string(filepath.Separator)+".sgwtmp"+string(filepath.Separator)) {
			u.Objects++
		}
		return nil
	})
	return u
}

func (t *usageTracker) refresh(ctx context.Context, p *platform.Platform) {
	m, err := scanDir(dataDir(p.DataRoot))
	if err != nil {
		return
	}
	t.mu.Lock()
	t.buckets, t.pending, t.measuredAt = m, map[string]int64{}, time.Now().UTC()
	t.mu.Unlock()
}

// invalidate asks the background loop for a fresh scan.
func (t *usageTracker) invalidate() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// add records a finished write that grew (or, negative, shrank) a bucket.
func (t *usageTracker) add(s3name string, n int64) {
	if n == 0 {
		return
	}
	t.mu.Lock()
	t.pending[s3name] += n
	t.mu.Unlock()
}

// reserve counts n bytes of an upload in progress until settle.
func (t *usageTracker) reserve(s3name string, n int64) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	t.inflight[s3name] += n
	t.mu.Unlock()
}

// settle ends a reservation of reserved bytes; a write that succeeded
// changed the bucket by delta.
func (t *usageTracker) settle(s3name string, reserved int64, ok bool, delta int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if reserved > 0 {
		if t.inflight[s3name] -= reserved; t.inflight[s3name] <= 0 {
			delete(t.inflight, s3name)
		}
	}
	if ok && delta != 0 {
		t.pending[s3name] += delta
	}
}

func (t *usageTracker) bucket(s3name string) Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.buckets[s3name]
	u.Bytes = max(0, u.Bytes+t.pending[s3name])
	return u
}

// project sums a project's buckets ("<project>--*"): the last scan, writes
// since and uploads in progress.
func (t *usageTracker) project(project string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var n int64
	for _, m := range []map[string]int64{t.pending, t.inflight} {
		for s3name, b := range m {
			if projectOf(s3name) == project {
				n += b
			}
		}
	}
	for s3name, u := range t.buckets {
		if projectOf(s3name) == project {
			n += u.Bytes
		}
	}
	return max(0, n)
}

func (t *usageTracker) at() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.measuredAt
}

// ---- quotas ----

// quotaFor returns a project's quota in bytes (0 means unlimited) and
// whether it was set for the project (rather than the box default).
func quotaFor(ctx context.Context, p *platform.Platform, project string) (int64, bool, error) {
	if raw, ok, err := p.DB.KVGet(ctx, kvNS, "quota/"+project); err != nil {
		return 0, false, err
	} else if ok {
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if n < 0 { // set to unlimited for this project
			n = 0
		}
		return n, true, err
	}
	d, err := defaultQuota(ctx, p)
	return d, false, err
}

func defaultQuota(ctx context.Context, p *platform.Platform) (int64, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "quota-default")
	if err != nil || !ok {
		return DefaultQuotaBytes, err
	}
	return strconv.ParseInt(string(raw), 10, 64)
}
