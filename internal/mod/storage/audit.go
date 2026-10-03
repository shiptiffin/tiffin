package storage

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// manifestEntry is one object in a checksum manifest.
type manifestEntry struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// audit reads every object of a project, checks single-part objects against
// their MD5 ETag and writes a SHA-256 manifest for verifying mirrors.
func (m *Module) audit(ctx context.Context, p *platform.Platform, project string) (*AuditReport, error) {
	start := time.Now()
	on, buckets, err := projectStorage(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, noStorage(project)
	}
	gw, err := m.gateway(p)
	if err != nil {
		return nil, gwProblem(err)
	}
	rep := &AuditReport{Project: project, Problems: []AuditIssue{},
		Backup: "no off-box mirror yet: the backup module will mirror " + dataDir(p.DataRoot) + " and verify it against this manifest"}
	manifest := map[string]map[string]manifestEntry{}
	names := make([]string, 0, len(buckets))
	for n := range buckets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		s3name := S3Name(project, name)
		if meta, _ := getMeta(ctx, p, s3name); meta == nil {
			continue // not created yet
		}
		entries := map[string]manifestEntry{}
		manifest[name] = entries
		token := ""
		for {
			page, err := gw.list(ctx, s3name, "", "", token, 1000)
			if err != nil {
				return nil, gwProblem(err)
			}
			for _, o := range page.Objects {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				rep.Objects++
				rep.Bytes += o.Size
				path := filepath.Join(dataDir(p.DataRoot), s3name, filepath.FromSlash(o.Key))
				md, sh, size, err := fileSums(path)
				switch {
				case errors.Is(err, os.ErrNotExist):
					rep.Problems = append(rep.Problems, AuditIssue{Bucket: name, Key: o.Key, Issue: "missing", Detail: "listed but no file at " + path})
					continue
				case err != nil:
					rep.Problems = append(rep.Problems, AuditIssue{Bucket: name, Key: o.Key, Issue: "unreadable", Detail: err.Error()})
					continue
				}
				entries[o.Key] = manifestEntry{SHA256: sh, Size: size}
				if strings.Contains(o.ETag, "-") {
					rep.Multipart++
					continue
				}
				if !strings.EqualFold(md, o.ETag) || size != o.Size {
					rep.Problems = append(rep.Problems, AuditIssue{Bucket: name, Key: o.Key, Issue: "checksum_mismatch",
						Detail: "recorded md5 " + o.ETag + ", on disk " + md})
					continue
				}
				rep.Verified++
			}
			if !page.Truncated || page.Next == "" {
				break
			}
			token = page.Next
		}
	}
	if err := os.MkdirAll(auditDir(p.DataRoot), 0o700); err != nil {
		return nil, err
	}
	rep.Manifest = filepath.Join(auditDir(p.DataRoot), project+".json")
	raw, _ := json.MarshalIndent(map[string]any{"project": project, "at": time.Now().UTC(), "buckets": manifest}, "", "  ")
	if err := os.WriteFile(rep.Manifest+".tmp", raw, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(rep.Manifest+".tmp", rep.Manifest); err != nil {
		return nil, err
	}
	rep.OK = len(rep.Problems) == 0
	rep.Took = time.Since(start).Round(time.Millisecond).String()
	return rep, nil
}

func fileSums(path string) (md5hex, sha string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	h1, h2 := md5.New(), sha256.New()
	size, err = io.Copy(io.MultiWriter(h1, h2), f)
	if err != nil {
		return "", "", 0, err
	}
	return hex.EncodeToString(h1.Sum(nil)), hex.EncodeToString(h2.Sum(nil)), size, nil
}
