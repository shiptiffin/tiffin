package valkey

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// "Delete all data" of a project's KV (resource emptied/valkey). Making the
// resource saves every key under the project's prefix (DUMP, with its
// expiry) to a file in <data root>/trash/kv, then deletes the keys.
// Deleting the resource (Restore, or undoing that change) deletes the keys
// there are by then and puts the saved ones back (RESTORE). The file is
// kept change.DataKeep, then deleted (pruneSaved). The project's ACL users
// and passwords are untouched: apps keep their REDIS_URL.

const nsEmptied = "valkey.emptied" // project → JSON emptiedRecord

type emptiedRecord struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
	File    string    `json:"file"`
	Keys    int       `json:"keys"`
	Done    bool      `json:"done"` // the keys were deleted after the save
}

// savedDir is where saved keys are kept until they expire.
func savedDir(p *platform.Platform) string {
	root := p.DataRoot
	if root == "" {
		root = "/var/lib/tiffin"
	}
	return filepath.Join(root, "trash", "kv")
}

// adminConn connects as the box's admin user (tests point it at their own server).
var adminConn = Admin

func reconcileEmptied(ctx context.Context, p *platform.Platform, project string, spec json.RawMessage) error {
	var rec emptiedRecord
	raw, have, err := p.DB.KVGet(ctx, nsEmptied, project)
	if err != nil {
		return err
	}
	if have {
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unreadable record of the deleted keys: %w", err)
		}
	}
	on, err := HasService(ctx, p, project)
	if err != nil {
		return err
	}
	if spec == nil {
		if !have {
			return nil
		}
		if on {
			if err := restoreKeys(ctx, p, project, rec); err != nil {
				return err
			}
		}
		_ = os.Remove(rec.File)
		return p.DB.KVDelete(ctx, nsEmptied, project)
	}
	var s change.EmptiedSpec
	if err := json.Unmarshal(spec, &s); err != nil {
		return fmt.Errorf("emptied spec: %w", err)
	}
	if have && rec.Version == s.Version && rec.Done {
		return nil
	}
	if !on {
		return errors.New("the project has no KV to empty")
	}
	c, err := adminConn(ctx)
	if err != nil {
		return fmt.Errorf("connect to valkey: %w", err)
	}
	defer c.Close()
	if !have || rec.Version != s.Version {
		if have {
			_ = os.Remove(rec.File) // an earlier delete's keys: replaced by this one
		}
		rec = emptiedRecord{Version: s.Version, At: time.Now().UTC(),
			File: filepath.Join(savedDir(p), project+"-"+strconv.FormatInt(s.Version, 10)+".kv")}
		n, err := saveKeys(ctx, c, Prefix(project), rec.File)
		if err != nil {
			return fmt.Errorf("save the keys before deleting them (nothing was deleted): %w", err)
		}
		rec.Keys = n
		if err := putEmptied(ctx, p, project, rec); err != nil {
			return err
		}
	}
	if _, err := deletePrefix(ctx, c, Prefix(project)); err != nil {
		return err
	}
	rec.Done = true
	p.Log.Info("valkey: deleted all keys", "project", project, "keys", rec.Keys)
	return putEmptied(ctx, p, project, rec)
}

func restoreKeys(ctx context.Context, p *platform.Platform, project string, rec emptiedRecord) error {
	c, err := adminConn(ctx)
	if err != nil {
		return fmt.Errorf("connect to valkey: %w", err)
	}
	defer c.Close()
	n, err := loadKeys(ctx, c, Prefix(project), rec.File)
	if errors.Is(err, os.ErrNotExist) {
		p.Log.Warn("valkey: the saved keys are gone; nothing to restore", "project", project, "file", rec.File)
		return nil
	}
	if err != nil {
		return err
	}
	p.Log.Info("valkey: restored the deleted keys", "project", project, "keys", n)
	return nil
}

func putEmptied(ctx context.Context, p *platform.Platform, project string, rec emptiedRecord) error {
	raw, _ := json.Marshal(rec)
	return p.DB.KVPut(ctx, nsEmptied, project, raw)
}

// The saved-keys file: "TKV1", then per key its name, its expiry (unix ms,
// 0 when it never expires) and its DUMP, each length-prefixed (uvarint).

const savedMagic = "TKV1"

// saveKeys writes every key under prefix to file and returns how many.
func saveKeys(ctx context.Context, c *Client, prefix, file string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return 0, err
	}
	tmp := file + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp)
	w := bufio.NewWriter(f)
	n, err := writeKeys(ctx, c, prefix, w)
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	return n, os.Rename(tmp, file)
}

func writeKeys(ctx context.Context, c *Client, prefix string, w io.Writer) (int, error) {
	if _, err := io.WriteString(w, savedMagic); err != nil {
		return 0, err
	}
	put := func(b []byte) error {
		var l [binary.MaxVarintLen64]byte
		if _, err := w.Write(l[:binary.PutUvarint(l[:], uint64(len(b)))]); err != nil {
			return err
		}
		_, err := w.Write(b)
		return err
	}
	n, cursor := 0, "0"
	for {
		v, err := c.Do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "1000")
		if err != nil {
			return n, err
		}
		next, keys := scanReply(v)
		cursor = next
		if len(keys) > 0 {
			cmds := make([][]string, 0, 2*len(keys))
			for _, k := range keys {
				cmds = append(cmds, []string{"DUMP", k}, []string{"PEXPIRETIME", k})
			}
			r, err := c.Pipe(ctx, cmds...)
			if err != nil {
				return n, err
			}
			for i, k := range keys {
				dump, ok := r[2*i].(string)
				if !ok {
					continue // gone since the scan
				}
				at, _ := r[2*i+1].(int64)
				if at < 0 {
					at = 0
				}
				if err := put([]byte(strings.TrimPrefix(k, prefix))); err != nil {
					return n, err
				}
				if err := put([]byte(strconv.FormatInt(at, 10))); err != nil {
					return n, err
				}
				if err := put([]byte(dump)); err != nil {
					return n, err
				}
				n++
			}
		}
		if cursor == "0" {
			return n, nil
		}
	}
}

// loadKeys deletes every key under prefix, then puts back the keys saved in
// file (an expiry that has passed meanwhile leaves its key out). It returns
// how many it put back.
func loadKeys(ctx context.Context, c *Client, prefix, file string) (int, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	magic := make([]byte, len(savedMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != savedMagic {
		return 0, fmt.Errorf("%s is not a saved-keys file", file)
	}
	if _, err := deletePrefix(ctx, c, prefix); err != nil {
		return 0, err
	}
	get := func() ([]byte, error) {
		l, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		if l > 1<<30 {
			return nil, fmt.Errorf("%s is damaged", file)
		}
		b := make([]byte, l)
		_, err = io.ReadFull(r, b)
		return b, err
	}
	n := 0
	now := time.Now().UnixMilli()
	for {
		key, err := get()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		atRaw, err := get()
		if err != nil {
			return n, err
		}
		dump, err := get()
		if err != nil {
			return n, err
		}
		at, _ := strconv.ParseInt(string(atRaw), 10, 64)
		args := []string{"RESTORE", prefix + string(key), "0", string(dump), "REPLACE"}
		if at > 0 {
			if at <= now {
				continue
			}
			args = []string{"RESTORE", prefix + string(key), strconv.FormatInt(at, 10), string(dump), "REPLACE", "ABSTTL"}
		}
		if _, err := c.Do(ctx, args...); err != nil {
			return n, fmt.Errorf("restore %s: %w", key, err)
		}
		n++
	}
}

// pruneSaved deletes saved keys older than change.DataKeep.
func pruneSaved(ctx context.Context, p *platform.Platform) {
	all, err := p.DB.KVList(ctx, nsEmptied)
	if err != nil {
		return
	}
	for project, raw := range all {
		var rec emptiedRecord
		if json.Unmarshal(raw, &rec) != nil || time.Since(rec.At) <= change.DataKeep {
			continue
		}
		if err := os.Remove(rec.File); err == nil {
			p.Log.Info("valkey: deleted saved keys past 7 days", "project", project, "keys", rec.Keys)
		}
	}
}
