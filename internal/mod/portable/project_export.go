package portable

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/storage"
	"github.com/btahir/tiffin/internal/mod/valkey"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// reporter receives a job's progress.
type reporter struct {
	phase func(phase string, percent int)
	bytes func(n int64) // archive content written or read so far
}

func (r reporter) say(phase string, percent int) {
	if r.phase != nil {
		r.phase(phase, percent)
	}
}

// exportOptions shape a project archive.
type exportOptions struct {
	includeSecrets bool // plain .env instead of sealed secrets.json
	withHistory    bool
	sameBox        bool   // a duplicate: images stay in the store (release.json names them)
	stage          string // a scratch directory for spooled parts
}

// written is what writeProject made.
type written struct {
	info   *ProjectInfo
	size   int64
	sha256 string
	parts  map[string]boxfile.Stats
}

// writeProject writes one project's archive to out.
func writeProject(ctx context.Context, p *platform.Platform, b backend, project string, o exportOptions, rep reporter, out io.Writer) (*written, error) {
	rep.say("looking at the project", 2)
	ver, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	if ver == 0 || len(res) == 0 {
		return nil, fmt.Errorf("%w: %s", errNotFoundProject, project)
	}
	m, err := change.ManifestFromResources(project, res)
	if err != nil {
		return nil, err
	}
	mraw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	info := &ProjectInfo{Project: project, Source: boxfile.Source{Domain: p.Domain, PublicURL: p.PublicURL, Hostname: hostname},
		TiffinVersion: p.Version, ExportedAt: time.Now().UTC(), Manifest: mraw, History: o.withHistory}
	if r, ok := res[change.KindStorageLimit]; ok {
		info.StorageLimit = r.Spec
	}
	secrets := map[string]json.RawMessage{}
	for addr, r := range res {
		if change.Kind(addr) == change.KindSecret {
			secrets[change.Name(addr)] = r.Spec
			info.Secrets.Names = append(info.Secrets.Names, change.Name(addr))
		}
	}
	sort.Strings(info.Secrets.Names)
	info.Secrets.Plain = o.includeSecrets && len(secrets) > 0
	if len(secrets) > 0 && !o.includeSecrets {
		if info.Secrets.Recipient, err = keyRecipient(p.Home); err != nil {
			return nil, fmt.Errorf("box key: %w", err)
		}
	}
	if m.Services.Postgres != nil {
		if info.Postgres, err = b.databaseInfo(ctx, project); err != nil {
			return nil, fmt.Errorf("postgres: %w", err)
		}
	}
	if m.Services.Valkey != nil {
		info.Valkey = &ValkeyInfo{Prefix: valkey.Prefix(project)}
	}
	if m.Services.Storage != nil {
		for _, name := range sortedKeys(m.Services.Storage.Buckets) {
			info.Buckets = append(info.Buckets, BucketInfo{Name: name, S3Name: storage.S3Name(project, name), Public: m.Services.Storage.Buckets[name].Public})
		}
	}
	releases := map[string]*appRelease{}
	sites := map[string]string{}
	for _, name := range sortedKeys(m.Apps) {
		a := m.Apps[name]
		ai := AppInfo{Name: name, Framework: string(a.Framework), Role: string(a.Role)}
		d, err := b.liveRelease(ctx, project, name)
		if err != nil {
			return nil, fmt.Errorf("app %s: %w", name, err)
		}
		if d != nil && (d.Image != "" || d.StaticRoot != "") {
			ai.Deploy, ai.URL, ai.Image, ai.Commit, ai.Repo = d.ID, d.URL, d.Image, d.Commit, d.Repo
			ai.ImageFile = d.Image != "" && !o.sameBox
			ai.Site = d.StaticRoot != ""
			releases[name] = &appRelease{App: name, Deploy: d.ID, Framework: d.Framework, Image: d.Image, Commit: d.Commit, Repo: d.Repo,
				Dir: d.Dir, Vercel: d.Vercel}
			if ai.Site {
				sites[name] = d.StaticRoot
			}
		}
		info.Apps = append(info.Apps, ai)
	}
	git := b.gitDir(project)
	if fi, err := os.Stat(filepath.Join(git, "HEAD")); err == nil && !fi.IsDir() {
		info.Git = true
	}

	man := &boxfile.Manifest{Kind: boxfile.ProjectKind, Project: project, Format: boxfile.FormatVersion, TiffinVersion: p.Version,
		Schema: state.SchemaVersion(), CreatedAt: info.ExportedAt, Source: info.Source, Projects: []string{project},
		Recipient: info.Secrets.Recipient, WithHistory: o.withHistory,
		Consistency: boxfile.Consistency{Note: "the database is one consistent snapshot (pg_dump); files and cache keys were read live"}}
	if info.Postgres != nil {
		man.Databases = []string{info.Postgres.Database}
	}
	for _, r := range releases {
		if r.Image != "" {
			man.Images = append(man.Images, r.Image)
		}
	}
	sort.Strings(man.Images)
	man.Parts = []string{"project"}
	if o.withHistory {
		man.Parts = append(man.Parts, "history")
	}
	if len(secrets) > 0 {
		man.Parts = append(man.Parts, "secrets")
	}
	if info.Postgres != nil {
		man.Parts = append(man.Parts, "postgres")
	}
	if info.Valkey != nil {
		man.Parts = append(man.Parts, "valkey")
	}
	if len(info.Buckets) > 0 {
		man.Parts = append(man.Parts, "files")
	}
	if info.Git {
		man.Parts = append(man.Parts, "git")
	}
	if len(releases) > 0 {
		man.Parts = append(man.Parts, "apps")
	}

	if err := os.MkdirAll(o.stage, 0o700); err != nil {
		return nil, err
	}
	aw, err := boxfile.NewWriter(out, man)
	if err != nil {
		return nil, err
	}
	defer aw.Abort()
	aw.Progress = rep.bytes

	// ---- what it is ----
	if err := aw.WriteJSON("project.json", info); err != nil {
		return nil, err
	}
	if err := aw.WriteBytes("README.md", []byte(readme(info)), 0o644); err != nil {
		return nil, err
	}
	note := fmt.Sprintf("Project %s, exported from %s on %s.", project, orDash(p.Domain), info.ExportedAt.Format("2006-01-02"))
	if err := aw.WriteBytes("tiffin.config.ts", manifest.RenderConfig(m, note), 0o644); err != nil {
		return nil, err
	}
	if err := aw.WriteBytes("docker-compose.yml", []byte(compose(info, m)), 0o644); err != nil {
		return nil, err
	}
	aw.Part("project", boxfile.Stats{Files: 4})

	// ---- History ----
	if o.withHistory {
		rep.say("writing the project's History", 5)
		path, err := spool(o.stage, "history", func(w io.Writer) error { return writeHistory(ctx, p.DB, project, w) })
		if err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
		n, err := aw.WriteFile("history/changes.jsonl", path)
		_ = os.Remove(path)
		if err != nil {
			return nil, err
		}
		aw.Part("history", boxfile.Stats{Files: 1, Bytes: n})
	}

	// ---- secrets ----
	if len(secrets) > 0 {
		if o.includeSecrets {
			plain := map[string]string{}
			for n, spec := range secrets {
				if plain[n], err = p.Secrets.OpenSpec(spec); err != nil {
					return nil, fmt.Errorf("secret %s: %w", n, err)
				}
			}
			if err := aw.WriteBytes(".env", []byte(dotenv(plain)), 0o600); err != nil {
				return nil, err
			}
		} else {
			ss := sealedSecrets{Recipient: info.Secrets.Recipient, Secrets: secrets,
				Note: "Each value is sealed (age) to the source box's key, " + info.Secrets.Recipient + ". `tiffin projects import` on that box reads them as they are; " +
					"another box needs that key (--secrets-key-file), or set them again by hand."}
			if err := aw.WriteJSON("secrets.json", ss); err != nil {
				return nil, err
			}
		}
		aw.Part("secrets", boxfile.Stats{Files: 1, Items: len(secrets)})
	}

	// ---- Postgres ----
	if info.Postgres != nil {
		rep.say("dumping the database", 10)
		if err := aw.WriteBytes("database-setup.sql", []byte(databaseSetup(info.Postgres)), 0o644); err != nil {
			return nil, err
		}
		if free := freeBytes(o.stage); free >= 0 && free < info.Postgres.SizeBytes+info.Postgres.SizeBytes/5+256<<20 {
			return nil, fmt.Errorf("not enough disk space to stage the database dump: %s free, the database is %s", human(free), human(info.Postgres.SizeBytes))
		}
		path, err := spool(o.stage, "database", func(w io.Writer) error { return b.dumpDatabase(ctx, project, w) })
		if err != nil {
			return nil, fmt.Errorf("postgres: %w", err)
		}
		rep.say("writing the database", 25)
		n, err := aw.WriteFile("database.sql", path)
		_ = os.Remove(path)
		if err != nil {
			return nil, err
		}
		aw.Part("postgres", boxfile.Stats{Files: 2, Bytes: n, Items: 1})
	}

	// ---- Valkey ----
	if info.Valkey != nil {
		rep.say("reading the cache", 35)
		keys := 0
		path, err := spool(o.stage, "cache", func(w io.Writer) error {
			bw := bufio.NewWriter(w)
			enc := json.NewEncoder(bw)
			if err := b.dumpKeys(ctx, project, func(e cacheEntry) error { keys++; return enc.Encode(e) }); err != nil {
				return err
			}
			return bw.Flush()
		})
		if err != nil {
			return nil, fmt.Errorf("valkey: %w", err)
		}
		n, err := aw.WriteFile("cache.jsonl", path)
		_ = os.Remove(path)
		if err != nil {
			return nil, err
		}
		aw.Part("valkey", boxfile.Stats{Files: 1, Bytes: n, Items: keys})
	}

	// ---- files ----
	if len(info.Buckets) > 0 {
		rep.say("writing the files", 45)
		var st boxfile.Stats
		for _, bk := range info.Buckets {
			s, err := aw.WriteTree("files/"+bk.Name, b.bucketDir(project, bk.Name), boxfile.TreeOptions{Skip: gatewayScratch})
			if err != nil {
				return nil, fmt.Errorf("bucket %s: %w", bk.Name, err)
			}
			st.Files += s.Files
			st.Bytes += s.Bytes
			st.Items++
		}
		aw.Part("files", st)
	}

	// ---- git ----
	if info.Git {
		rep.say("writing the git repository", 60)
		st, err := aw.WriteTree("source.git", git, boxfile.TreeOptions{})
		if err != nil {
			return nil, fmt.Errorf("git: %w", err)
		}
		aw.Part("git", st)
	}

	// ---- apps ----
	if len(releases) > 0 {
		var st boxfile.Stats
		for _, name := range sortedKeys(releases) {
			r := releases[name]
			rep.say("writing app "+name, 70)
			if err := aw.WriteJSON("apps/"+name+"/release.json", r); err != nil {
				return nil, err
			}
			st.Items++
			if root, ok := sites[name]; ok {
				s, err := aw.WriteTree("apps/"+name+"/site", root, boxfile.TreeOptions{})
				if err != nil {
					return nil, fmt.Errorf("app %s: %w", name, err)
				}
				st.Files += s.Files
				st.Bytes += s.Bytes
			} else if r.Image != "" && !o.sameBox {
				path, err := spool(o.stage, "image", func(w io.Writer) error { return b.saveImage(ctx, r.Image, w) })
				if err != nil {
					return nil, fmt.Errorf("app %s: save its image: %w", name, err)
				}
				n, err := aw.WriteFile("apps/"+name+"/image.tar", path)
				_ = os.Remove(path)
				if err != nil {
					return nil, err
				}
				st.Files++
				st.Bytes += n
			}
		}
		aw.Part("apps", st)
	}

	rep.say("finishing", 95)
	size, sum, tr, err := aw.Close()
	if err != nil {
		return nil, err
	}
	return &written{info: info, size: size, sha256: sum, parts: tr.Parts}, nil
}

// gatewayScratch leaves out the object store's own scratch space (uploads
// in progress).
func gatewayScratch(rel string, _ fs.DirEntry) bool {
	return strings.HasPrefix(strings.SplitN(rel, "/", 2)[0], ".sgwtmp")
}

// writeHistory writes the project's changes, oldest first, one per line.
func writeHistory(ctx context.Context, db *state.DB, project string, w io.Writer) error {
	var all []*change.Change
	var before int64
	for {
		cs, err := db.ListChanges(ctx, change.ListFilter{Project: project, Limit: 200, Before: before})
		if err != nil {
			return err
		}
		all = append(all, cs...)
		if len(cs) < 200 {
			break
		}
		if before, err = db.ChangeSeq(ctx, cs[len(cs)-1].ID); err != nil {
			return err
		}
	}
	slices.Reverse(all)
	enc := json.NewEncoder(w)
	for _, c := range all {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	return nil
}

// dotenv renders KEY="value" lines (\n, " and \ escaped), sorted.
func dotenv(env map[string]string) string {
	var b strings.Builder
	b.WriteString("# Secrets of this project, in plain text. Keep this file private.\n")
	for _, k := range sortedKeys(env) {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(env[k])
		b.WriteString(k + `="` + v + "\"\n")
	}
	return b.String()
}

// parseDotenv reads what dotenv writes.
func parseDotenv(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			var b strings.Builder
			for i := 1; i < len(v)-1; i++ {
				if v[i] == '\\' && i+1 < len(v)-1 {
					i++
					switch v[i] {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					default:
						b.WriteByte(v[i])
					}
					continue
				}
				b.WriteByte(v[i])
			}
			v = b.String()
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
