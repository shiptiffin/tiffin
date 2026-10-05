package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/dnskit"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// projectJob mirrors the API's project job (duplicate or import).
type projectJob struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	From    string `json:"from"`
	Project string `json:"project"`
	Source  *struct {
		Project      string   `json:"project"`
		Domain       string   `json:"domain"`
		Secrets      []string `json:"secrets"`
		SecretsPlain bool     `json:"secretsPlain"`
		SecretsHere  bool     `json:"secretsHere"`
		Recipient    string   `json:"recipient"`
		Taken        bool     `json:"taken"`
	} `json:"source,omitempty"`
	SizeBytes      int64        `json:"sizeBytes,omitempty"`
	SHA256         string       `json:"sha256,omitempty"`
	DurationMs     int64        `json:"durationMs,omitempty"`
	Created        bool         `json:"created"`
	Change         string       `json:"change,omitempty"`
	Apps           []appAddress `json:"apps,omitempty"`
	SecretsMissing []string     `json:"secretsMissing,omitempty"`
	Notes          []string     `json:"notes,omitempty"`
	Healthy        bool         `json:"healthy"`
	Error          string       `json:"error,omitempty"`
	Hint           string       `json:"hint,omitempty"`
}

// appAddress is an app and where it answers.
type appAddress struct {
	App    string `json:"app"`
	Deploy string `json:"deploy,omitempty"`
	Status string `json:"status"`
	URL    string `json:"url,omitempty"`
	Error  string `json:"error,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// projectExport mirrors the API's project export.
type projectExport struct {
	ID          string                   `json:"id"`
	Project     string                   `json:"project"`
	Status      string                   `json:"status"`
	Phase       string                   `json:"phase"`
	SizeBytes   int64                    `json:"sizeBytes"`
	SHA256      string                   `json:"sha256"`
	DurationMs  int64                    `json:"durationMs"`
	Parts       map[string]boxfile.Stats `json:"parts"`
	Secrets     []string                 `json:"secrets"`
	SecretsNote string                   `json:"secretsNote"`
	Apps        []appAddress             `json:"apps"`
	Download    string                   `json:"download"`
	FileName    string                   `json:"fileName"`
	Error       string                   `json:"error"`
	Hint        string                   `json:"hint"`
}

// projectsCmd is the projects group's hand-written commands; the generated
// ones (list, get, destroy, stop, start...) join them.
func (a *app) projectsCmd() *cobra.Command {
	c := &cobra.Command{Use: "projects", Short: groupShort["projects"]}
	c.AddCommand(a.projectDuplicateCmd(), a.projectExportCmd(), a.projectImportCmd(), a.projectMoveCmd())
	return c
}

func (a *app) projectDuplicateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "duplicate <project> <new-name>",
		Short: "Copy a project on this box under a new name",
		Long: "Makes a full copy of a project on this box: its database, buckets and files, cache keys, secrets, settings and apps " +
			"(started from the same images or files at the new project's own addresses: shop → shop-copy). Custom domains and GitHub " +
			"deploys stay with the original; the copy starts its own History. Waits until the copy's apps are live. To undo it, destroy the copy.",
		Example: "  tiffin projects duplicate shop shop-copy",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start := time.Now()
			c, err := a.remoteClient(ctx, "projects duplicate")
			if err != nil {
				return err
			}
			defer c.close()
			status, raw, err := c.do(ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(args[0])+"/duplicate", nil, map[string]any{"name": args[1]})
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status != http.StatusAccepted {
				return problemOf(status, raw)
			}
			var job projectJob
			if err := json.Unmarshal(raw, &job); err != nil {
				return err
			}
			if err := a.waitProjectJob(ctx, c, &job); err != nil {
				return err
			}
			return a.reportJob(&job, fmt.Sprintf("Duplicated %s as %s", job.From, job.Project), start)
		},
	}
}

// waitProjectJob polls a job until it is done or failed.
func (a *app) waitProjectJob(ctx context.Context, c *client, job *projectJob) error {
	var mu sync.Mutex
	pl := a.startProgress(func() string {
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("%s (%d%%)", orDefault(job.Phase, job.Status), job.Percent)
	})
	defer pl.end()
	for job.Status != "done" && job.Status != "failed" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		var cur projectJob
		if err := a.getJSON(ctx, c, "/v1/project-jobs/"+job.ID, &cur); err != nil {
			return err
		}
		mu.Lock()
		*job = cur
		mu.Unlock()
	}
	return nil
}

// reportJob prints a finished duplicate or import.
func (a *app) reportJob(job *projectJob, done string, start time.Time) error {
	if job.Status == "failed" && !job.Created {
		return &exitError{ExitError, job.Kind + " " + job.ID + " failed: " + job.Error + hintOf(job.Hint)}
	}
	// Failed with the project created (an app did not start, say): the
	// project is there, so show what came across, and exit non-zero.
	if !job.Healthy || job.Status == "failed" {
		a.code = ExitError
	}
	if !a.tty() {
		writeJSON(a.io.Out, job)
		return nil
	}
	w := a.io.Out
	mark := a.paint("✓", green)
	switch {
	case job.Status == "failed":
		mark, done = a.paint("✗", red), done+", but not everything came across"
	case !job.Healthy:
		mark = a.paint("!", amber)
	}
	fmt.Fprintf(w, "%s %s %s\n", mark, done, a.paint("("+time.Since(start).Round(time.Second).String()+")", dim))
	for _, ap := range job.Apps {
		switch ap.Status {
		case "live":
			fmt.Fprintf(w, "  %-10s %s\n", ap.App, orDefault(ap.URL, "live"))
		case "none":
			fmt.Fprintf(w, "  %-10s %s\n", ap.App, a.paint("no release to bring (deploy it)", dim))
		default:
			fmt.Fprintf(w, "  %-10s %s %s\n", ap.App, a.paint("✗", red), ap.Error)
		}
	}
	for _, n := range job.Notes {
		fmt.Fprintf(w, "  %s %s\n", a.paint("·", dim), n)
	}
	if job.Hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", job.Hint)
	}
	return nil
}

func (a *app) projectExportCmd() *cobra.Command {
	var out string
	var includeSecrets, withHistory bool
	cmd := &cobra.Command{
		Use:   "export <project>",
		Short: "Export one project to a .tiffin file",
		Long: "Writes one project to a single .tiffin file (a zstd-compressed tar) of ordinary files: database.sql (pg_dump), " +
			"files/<bucket>/..., the cache keys, each app's image (docker load) or static files, the git repository, " +
			"tiffin.config.ts, project.json, and a docker-compose.yml with a README to run it without Tiffin. It streams to your " +
			"computer as it is made; the box needs no extra disk. Import it with `tiffin projects import`, here or on another box.\n\n" +
			"Secrets stay sealed to this box's key (only this box can read them) unless --include-secrets puts them inside in plain " +
			"text (.env): then anyone with the file can read them.",
		Example: "  tiffin projects export shop\n  tiffin projects export shop -o shop.tiffin --with-history\n  tiffin projects export shop --include-secrets",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start := time.Now()
			c, err := a.remoteClient(ctx, "projects export")
			if err != nil {
				return err
			}
			defer c.close()
			status, raw, err := c.do(ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(args[0])+"/exports", nil,
				map[string]any{"includeSecrets": includeSecrets, "withHistory": withHistory})
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status != http.StatusOK {
				return problemOf(status, raw)
			}
			var ex projectExport
			if err := json.Unmarshal(raw, &ex); err != nil {
				return err
			}
			path := orDefault(out, ex.FileName)
			if _, err := os.Stat(path); err == nil {
				return &exitError{ExitInvalid, path + " already exists; pass another file name with -o"}
			}
			record := "/v1/projects/" + url.PathEscape(ex.Project) + "/exports/" + ex.ID
			n, sum, err := a.downloadArchive(ctx, c, ex.Download, record, path)
			if err != nil {
				return err
			}
			for i := 0; i < 50 && ex.Status != "done" && ex.Status != "failed"; i++ {
				if err := a.getJSON(ctx, c, record, &ex); err != nil {
					return err
				}
				if ex.Status != "done" {
					time.Sleep(200 * time.Millisecond)
				}
			}
			if ex.Status != "done" || ex.SHA256 != sum || ex.SizeBytes != n {
				_ = os.Remove(path)
				if ex.Status != "done" {
					return &exitError{ExitError, "export " + ex.ID + " " + ex.Status + ": " + ex.Error + hintOf(ex.Hint)}
				}
				return &exitError{ExitError, fmt.Sprintf("the download does not match what the box wrote (%d bytes, sha256 %s; the box says %d, %s); try again", n, sum, ex.SizeBytes, ex.SHA256)}
			}
			if !a.tty() {
				writeJSON(a.io.Out, map[string]any{"export": ex.ID, "project": ex.Project, "file": path, "sizeBytes": n, "sha256": sum,
					"seconds": round1s(time.Since(start).Seconds()), "parts": ex.Parts, "secrets": ex.Secrets, "secretsNote": ex.SecretsNote, "apps": ex.Apps})
				return nil
			}
			w := a.io.Out
			fmt.Fprintf(w, "%s Exported %s to %s %s\n\n", a.paint("✓", green), ex.Project, a.paint(path, bold),
				a.paint(fmt.Sprintf("(%s in %s)", humanSize(n), time.Since(start).Round(100*time.Millisecond)), dim))
			fmt.Fprintf(w, "  %-9s %s\n", "SHA-256", sum)
			fmt.Fprintf(w, "  %-9s %s\n", "Contains", describeProjectParts(ex.Parts))
			if ex.SecretsNote != "" {
				fmt.Fprintf(w, "  %-9s %s\n", "Secrets", strings.Join(ex.Secrets, ", "))
				mark := a.paint("·", dim)
				if includeSecrets {
					mark = a.paint("!", amber)
				}
				fmt.Fprintf(w, "\n%s %s\n", mark, ex.SecretsNote)
			}
			fmt.Fprintf(w, "\n  Run it without Tiffin: unpack it (tar --zstd -xf %s) and read README.md.\n", path)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&out, "output", "o", "", "the file to write (default <project>-<time>.tiffin)")
	f.BoolVar(&includeSecrets, "include-secrets", false, "put the secrets inside in plain text (.env): anyone with the file can read them")
	f.BoolVar(&withHistory, "with-history", false, "also export the project's History (every change)")
	return cmd
}

func describeProjectParts(parts map[string]boxfile.Stats) string {
	var out []string
	if _, ok := parts["postgres"]; ok {
		out = append(out, "database")
	}
	if s, ok := parts["files"]; ok {
		out = append(out, fmt.Sprintf("%d bucket(s), %d file(s)", s.Items, s.Files))
	}
	if s, ok := parts["valkey"]; ok {
		out = append(out, fmt.Sprintf("%d cache key(s)", s.Items))
	}
	if s, ok := parts["apps"]; ok {
		out = append(out, fmt.Sprintf("%d app release(s)", s.Items))
	}
	if _, ok := parts["git"]; ok {
		out = append(out, "git repository")
	}
	if s, ok := parts["secrets"]; ok {
		out = append(out, fmt.Sprintf("%d secret(s)", s.Items))
	}
	if _, ok := parts["history"]; ok {
		out = append(out, "History")
	}
	if len(out) == 0 {
		return "the project's settings"
	}
	return strings.Join(out, ", ")
}

// readSecretsKey reads an age key file (the source box's secrets.key).
func readSecretsKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", &exitError{ExitInvalid, "--secrets-key-file: " + err.Error()}
	}
	key := strings.TrimSpace(string(raw))
	if _, err := age.ParseX25519Identity(key); err != nil {
		return "", &exitError{ExitInvalid, "--secrets-key-file: not an age secret key: " + err.Error()}
	}
	return key, nil
}

func (a *app) projectImportCmd() *cobra.Command {
	var name, keyFile string
	var withoutSecrets bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import a project from a .tiffin file, as a new project",
		Long: "Uploads a project archive made by `tiffin projects export`, verifies it on the box and makes a new project from it, " +
			"beside the others: its database, files, cache, secrets and settings, then its apps from the archive's images or files " +
			"(and its History, if exported). It never replaces a project: a name in use is refused, so pass --name.\n\n" +
			"Under another name the apps get the new project's own addresses, and custom domains and GitHub deploys are left out. " +
			"Secrets sealed to another box's key need that box's key file (its " + boxKeyPath + "): --secrets-key-file; or " +
			"--without-secrets imports the rest and lists the secrets to set again.",
		Example: "  tiffin projects import shop.tiffin\n  tiffin projects import shop.tiffin --name shop-2\n  tiffin projects import shop.tiffin --secrets-key-file old-box.key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start := time.Now()
			path := args[0]
			f, err := os.Open(path)
			if err != nil {
				return &exitError{ExitInvalid, err.Error()}
			}
			defer f.Close()
			fi, err := f.Stat()
			if err != nil {
				return err
			}
			ar, err := boxfile.NewReader(f)
			if err != nil {
				return &exitError{ExitInvalid, path + ": " + err.Error()}
			}
			man := ar.Manifest
			ar.Close()
			switch {
			case man.Kind == boxfile.ManifestKind:
				return &exitError{ExitInvalid, path + " is an export of a whole box: import it with `tiffin box import`"}
			case man.Format > boxfile.FormatVersion:
				return &exitError{ExitInvalid, fmt.Sprintf("%s was made by a newer Tiffin (archive format %d); update this CLI and the box first", path, man.Format)}
			}
			key := ""
			if keyFile != "" {
				if key, err = readSecretsKey(keyFile); err != nil {
					return err
				}
			}
			c, err := a.remoteClient(ctx, "projects import")
			if err != nil {
				return err
			}
			defer c.close()
			target := orDefault(name, man.Project)
			if err := importNameFree(ctx, c, target, "an import never replaces a project; pass another name: tiffin projects import "+path+" --name "+target+"-2"); err != nil {
				return err
			}
			a.say("Archive %s: project %s from %s (Tiffin %s, %s), %s", path, man.Project, orDefault(man.Source.Domain, "?"), man.TiffinVersion,
				man.CreatedAt.Format("2006-01-02 15:04"), humanSize(fi.Size()))
			var job projectJob
			if err := a.uploadArchive(ctx, c, "/v1/project-imports", f, fi.Size(), &job); err != nil {
				return err
			}
			discard := func() {
				_, _, _ = c.do(context.WithoutCancel(ctx), http.MethodDelete, "/v1/project-imports/"+job.ID, nil, nil)
			}
			if s := job.Source; s != nil && len(s.Secrets) > 0 && !s.SecretsPlain && !s.SecretsHere && key == "" && !withoutSecrets {
				discard()
				return &exitError{ExitInvalid, fmt.Sprintf("the archive's secrets (%s) are sealed to another box's key (%s)", strings.Join(s.Secrets, ", "), s.Recipient) +
					hintOf("pass that box's key with --secrets-key-file <file> (its "+boxKeyPath+"), or --without-secrets to set them again by hand")}
			}
			body := map[string]any{"withoutSecrets": withoutSecrets}
			if name != "" {
				body["name"] = name
			}
			if key != "" {
				body["secretsKey"] = key
			}
			status, raw, err := c.do(ctx, http.MethodPost, "/v1/project-imports/"+job.ID+"/apply", nil, body)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status != http.StatusAccepted {
				discard()
				return problemOf(status, raw)
			}
			if err := json.Unmarshal(raw, &job); err != nil {
				return err
			}
			if err := a.waitProjectJob(ctx, c, &job); err != nil {
				return err
			}
			if len(job.SecretsMissing) > 0 {
				a.code = ExitError
			}
			return a.reportJob(&job, "Imported "+job.From+" as "+job.Project, start)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "the new project's name (default the archive's)")
	f.StringVar(&keyFile, "secrets-key-file", "", "the source box's key ("+boxKeyPath+" there), for secrets sealed to it")
	f.BoolVar(&withoutSecrets, "without-secrets", false, "import without the secrets (they are listed, to set by hand)")
	return cmd
}

// importNameFree asks the box, before anything is uploaded, whether a
// project can be imported there as name. One that exists or was destroyed
// less than 7 days ago is refused with hint; so is one beyond the token.
func importNameFree(ctx context.Context, c *client, name, hint string) error {
	status, raw, err := c.do(ctx, http.MethodPost, "/v1/project-imports", url.Values{"check": {"true"}, "name": {name}}, nil)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusConflict:
		var p api.Problem
		_ = json.Unmarshal(raw, &p)
		return &exitError{ExitInvalid, p.Detail + hintOf(hint)}
	case status == http.StatusUnprocessableEntity && strings.Contains(string(raw), "not a Tiffin export"):
		// A box older than the check took it for an empty upload: it can
		// only say which projects it has.
		st, raw, err := c.do(ctx, http.MethodGet, "/v1/projects", nil, nil)
		if err != nil {
			return &exitError{ExitError, err.Error()}
		}
		if st != http.StatusOK {
			return problemOf(st, raw)
		}
		var projects []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &projects)
		for _, p := range projects {
			if p.Name == name {
				return &exitError{ExitInvalid, "project " + name + " already exists on this box" + hintOf(hint)}
			}
		}
		return nil
	}
	return problemOf(status, raw)
}

// ---- move ----

// moveResult is what a move did.
type moveResult struct {
	Project   string        `json:"project"`
	From      string        `json:"from" doc:"the old box"`
	To        string        `json:"to" doc:"the new box"`
	Seconds   float64       `json:"seconds"`
	SizeBytes int64         `json:"sizeBytes"`
	SHA256    string        `json:"sha256"`
	Addresses []moveAddress `json:"addresses"`
	Domains   []moveDomain  `json:"domains,omitempty"`
	Stopped   bool          `json:"stoppedOnOldBox"`
	Import    *projectJob   `json:"import"`
	Next      string        `json:"next"`
}

type moveAddress struct {
	App    string `json:"app"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Status string `json:"status"`
}

type moveDomain struct {
	Domain    string          `json:"domain"`
	ManagedBy string          `json:"managedBy,omitempty"`
	Updated   bool            `json:"updated" doc:"the new box set its DNS records"`
	Records   []dnskit.Record `json:"records" doc:"point the name at the new box with these"`
}

// moveOptions shape a move.
type moveOptions struct {
	project   string
	fromName  string // the old box's name, for messages
	toName    string
	updateDNS bool
	progress  func(string)
}

// moveProject streams project's export from src straight into an import
// on dst (nothing lands on this computer), then stops it on src.
func moveProject(ctx context.Context, src, dst *client, o moveOptions) (*moveResult, error) {
	start := time.Now()
	say := func(s string) {
		if o.progress != nil {
			o.progress(s)
		}
	}
	res := &moveResult{Project: o.project, From: o.fromName, To: o.toName}
	if strings.TrimRight(src.base, "/") == strings.TrimRight(dst.base, "/") {
		return nil, &exitError{ExitInvalid, "project " + o.project + " is already on " + o.toName}
	}
	get := func(c *client, path string, into any) error {
		status, raw, err := c.do(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			return &exitError{ExitError, err.Error()}
		}
		if status != http.StatusOK {
			return problemOf(status, raw)
		}
		return json.Unmarshal(raw, into)
	}
	if err := importNameFree(ctx, dst, o.project, "a move keeps the project's name and never replaces a project, so the name must be free on "+o.toName); err != nil {
		return nil, fmt.Errorf("%s: %w", o.toName, err)
	}

	// The export: secrets in plain text, since only this box could open
	// sealed ones; they go from box to box and are never written here.
	say("starting the export on " + o.fromName)
	status, raw, err := src.do(ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(o.project)+"/exports", nil, map[string]any{"includeSecrets": true, "withHistory": true})
	if err != nil {
		return nil, &exitError{ExitError, err.Error()}
	}
	if status != http.StatusOK {
		return nil, problemOf(status, raw)
	}
	var ex projectExport
	if err := json.Unmarshal(raw, &ex); err != nil {
		return nil, err
	}
	record := "/v1/projects/" + url.PathEscape(o.project) + "/exports/" + ex.ID
	dl, err := src.stream(ctx, http.MethodGet, ex.Download, nil, "", "application/octet-stream")
	if err != nil {
		return nil, &exitError{ExitError, err.Error()}
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(dl.Body, 1<<20))
		return nil, problemOf(dl.StatusCode, b)
	}
	h := sha256.New()
	var n int64
	var mu sync.Mutex
	body := &countingReader{r: io.TeeReader(dl.Body, h), f: func(k int64) { mu.Lock(); n += k; mu.Unlock() }}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
				mu.Lock()
				say(fmt.Sprintf("copying %s from %s to %s", humanSize(n), o.fromName, o.toName))
				mu.Unlock()
			}
		}
	}()
	up, err := dst.stream(ctx, http.MethodPost, "/v1/project-imports", body, "application/octet-stream", "application/json")
	close(stop)
	if err != nil {
		return nil, &exitError{ExitError, "upload to " + o.toName + ": " + err.Error()}
	}
	raw, _ = io.ReadAll(io.LimitReader(up.Body, 4<<20))
	up.Body.Close()
	if up.StatusCode != http.StatusOK {
		return nil, problemOf(up.StatusCode, raw)
	}
	var job projectJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	discard := func() {
		_, _, _ = dst.do(context.WithoutCancel(ctx), http.MethodDelete, "/v1/project-imports/"+job.ID, nil, nil)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	for i := 0; i < 50 && ex.Status != "done" && ex.Status != "failed"; i++ {
		if err := get(src, record, &ex); err != nil {
			discard()
			return nil, err
		}
		if ex.Status != "done" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if ex.Status != "done" || ex.SHA256 != sum || job.SHA256 != sum {
		discard()
		return nil, &exitError{ExitError, fmt.Sprintf("the export did not arrive whole (%s: %s, sha256 %s; sent %s; %s got %s)", o.fromName, ex.Status, ex.SHA256, sum, o.toName, job.SHA256)}
	}
	res.SizeBytes, res.SHA256 = n, sum

	say("importing on " + o.toName)
	status, raw, err = dst.do(ctx, http.MethodPost, "/v1/project-imports/"+job.ID+"/apply", nil, map[string]any{"intent": "Moved from " + o.fromName})
	if err != nil {
		discard()
		return nil, &exitError{ExitError, err.Error()}
	}
	if status != http.StatusAccepted {
		discard()
		return nil, problemOf(status, raw)
	}
	_ = json.Unmarshal(raw, &job)
	for job.Status != "done" && job.Status != "failed" {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		if err := get(dst, "/v1/project-jobs/"+job.ID, &job); err != nil {
			return nil, err
		}
		say(fmt.Sprintf("%s on %s (%d%%)", orDefault(job.Phase, job.Status), o.toName, job.Percent))
	}
	res.Import = &job
	// An app that did not start fails the import (older boxes said done but
	// not healthy): the project keeps running here.
	if job.Status == "failed" || !job.Healthy {
		discard() // it holds the secrets in plain text
		return res, &exitError{ExitError, "the import on " + o.toName + " failed: " + job.Error + hintOf(job.Hint) +
			"\n  " + o.project + " still runs on " + o.fromName + ", untouched."}
	}
	before := map[string]string{}
	for _, ap := range ex.Apps {
		before[ap.App] = ap.URL
	}
	for _, ap := range job.Apps {
		res.Addresses = append(res.Addresses, moveAddress{App: ap.App, Before: before[ap.App], After: ap.URL, Status: ap.Status})
	}

	// Custom domains now point at the old box: re-point them.
	var domains []struct {
		Domain    string          `json:"domain"`
		ManagedBy string          `json:"managedBy"`
		Records   []dnskit.Record `json:"records"`
	}
	if err := get(dst, "/v1/projects/"+url.PathEscape(o.project)+"/domains", &domains); err == nil {
		for _, d := range domains {
			md := moveDomain{Domain: d.Domain, ManagedBy: d.ManagedBy, Records: d.Records}
			if d.ManagedBy != "" && o.updateDNS && len(d.Records) > 0 {
				say("updating DNS for " + d.Domain)
				st, raw, err := dst.do(ctx, http.MethodPut, "/v1/dns/records", nil, map[string]any{"records": d.Records})
				md.Updated = err == nil && st == http.StatusOK
				if !md.Updated && err == nil {
					say("could not update DNS for " + d.Domain + ": " + problemOf(st, raw).Error())
				}
			}
			res.Domains = append(res.Domains, md)
		}
	}

	say("stopping " + o.project + " on " + o.fromName)
	st, raw, err := src.do(ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(o.project)+"/stop", nil, map[string]any{"intent": "Moved to " + o.toName})
	res.Stopped = err == nil && st == http.StatusOK
	res.Next = "When you are happy with " + o.project + " on " + o.toName + ", destroy it on " + o.fromName + ": tiffin projects destroy " + o.project +
		" (against " + o.fromName + "). Until then it stays there, stopped; tiffin projects start " + o.project + " brings it back."
	if !res.Stopped {
		msg := "?"
		if err != nil {
			msg = err.Error()
		} else {
			msg = problemOf(st, raw).Error()
		}
		res.Next = "Stopping " + o.project + " on " + o.fromName + " failed (" + msg + "): stop it there yourself with tiffin projects stop " + o.project +
			", so it does not run twice. " + res.Next
	}
	res.Seconds = round1s(time.Since(start).Seconds())
	return res, nil
}

// moveClients are the clients of the current box and of the box named to.
func (a *app) moveClients(ctx context.Context, to string) (src, dst *client, fromName string, err error) {
	src, err = a.remoteClient(ctx, "projects move")
	if err != nil {
		return nil, nil, "", err
	}
	fromName, _ = a.currentBox()
	if a.url != "" {
		if u, err := url.Parse(a.url); err == nil {
			fromName = u.Hostname()
		}
	}
	f, err := a.loadBoxes()
	if err != nil {
		return nil, nil, "", err
	}
	bx := f.Boxes[to]
	if bx == nil {
		names := make([]string, 0, len(f.Boxes))
		for n := range f.Boxes {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, nil, "", &exitError{ExitInvalid, "no box named " + to + " in " + a.boxesPath() + hintOf("boxes this CLI knows: "+orDefault(strings.Join(names, ", "), "none")+"; create one with tiffin up")}
	}
	tok := bx.Token
	if a.token == "" && a.agentRun() && bx.AgentToken != "" {
		tok = bx.AgentToken
	}
	dst, err = a.boxClient(bx, tok)
	if err != nil {
		return nil, nil, "", err
	}
	return src, dst, orDefault(fromName, "this box"), nil
}

// addMoveTool gives `tiffin mcp` a project_move tool when this CLI knows
// another box: a move spans two boxes, so no single box's API can offer it.
// It acts as each box's agent key.
func (a *app) addMoveTool(srv *sdk.Server, current string, bx *boxConfig) {
	f, err := a.loadBoxes()
	if err != nil || bx == nil || bx.AgentToken == "" {
		return
	}
	var others []string
	for n, b := range f.Boxes {
		if n != current && b.AgentToken != "" {
			others = append(others, n)
		}
	}
	if len(others) == 0 {
		return
	}
	sort.Strings(others)
	to, _ := json.Marshal(others)
	schema := json.RawMessage(`{"type":"object","properties":{` +
		`"project":{"type":"string","pattern":"^[a-z][a-z0-9-]{0,39}$","description":"The project to move"},` +
		`"to":{"type":"string","enum":` + string(to) + `,"description":"The box to move it to"},` +
		`"updateDns":{"type":"boolean","description":"Let the new box set custom domains' DNS records where its DNS provider holds the zone"}},` +
		`"required":["project","to"],"additionalProperties":false}`)
	t, d := true, false
	srv.AddTool(&sdk.Tool{Name: "project_move", Title: "Move a project to another box", InputSchema: schema,
		Description: "Moves a project from this box (" + current + ") to another box this CLI knows: its export streams straight into an import " +
			"there (database, files, cache, secrets, settings, History, apps), then the project here is stopped (not destroyed). Returns the " +
			"apps' old and new addresses, custom domains (and their DNS records), and the next step: destroying it here once the move is checked. " +
			"Takes as long as copying the project's data.",
		Annotations: &sdk.ToolAnnotations{Title: "Move a project to another box", DestructiveHint: &t, OpenWorldHint: &d}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var in struct {
				Project   string `json:"project"`
				To        string `json:"to"`
				UpdateDNS bool   `json:"updateDns"`
			}
			fail := func(msg string) *sdk.CallToolResult {
				return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
			}
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil || in.Project == "" || f.Boxes[in.To] == nil {
				return fail("pass {\"project\": \"...\", \"to\": one of " + string(to) + "}"), nil
			}
			src, err := a.boxClient(bx, bx.AgentToken)
			if err != nil {
				return fail(err.Error()), nil
			}
			dst, err := a.boxClient(f.Boxes[in.To], f.Boxes[in.To].AgentToken)
			if err != nil {
				return fail(err.Error()), nil
			}
			src.session, dst.session = orDefault(a.session, "mcp:project_move"), orDefault(a.session, "mcp:project_move")
			res, err := moveProject(ctx, src, dst, moveOptions{project: in.Project, fromName: current, toName: in.To, updateDNS: in.UpdateDNS})
			if err != nil {
				return fail(err.Error()), nil
			}
			raw, _ := json.Marshal(res)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}}}, nil
		})
}

func (a *app) projectMoveCmd() *cobra.Command {
	var to string
	var updateDNS bool
	cmd := &cobra.Command{
		Use:   "move <project> --to <box>",
		Short: "Move a project to another of your boxes",
		Long: "Copies a project from this box to another box this CLI knows (a name in boxes.json, from `tiffin up`): the export streams " +
			"straight from one box into an import on the other, so nothing is stored on this computer. On success it prints how the " +
			"addresses change, re-points custom domains (with --update-dns the new box sets their DNS records when its DNS provider " +
			"holds the zone; otherwise it prints the records to set), and stops the project here: stopped, not destroyed. " +
			"Destroy it here once you are happy with the move.",
		Example: "  tiffin projects move shop --to prod\n  tiffin projects move shop --to prod --update-dns",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if to == "" {
				return &exitError{ExitInvalid, "--to <box> is required"}
			}
			src, dst, from, err := a.moveClients(ctx, to)
			if err != nil {
				return err
			}
			defer src.close()
			var mu sync.Mutex
			phase := "starting"
			pl := a.startProgress(func() string { mu.Lock(); defer mu.Unlock(); return phase })
			res, err := moveProject(ctx, src, dst, moveOptions{project: args[0], fromName: from, toName: to, updateDNS: updateDNS,
				progress: func(s string) { mu.Lock(); phase = s; mu.Unlock() }})
			pl.end()
			if err != nil {
				return err
			}
			if !a.tty() {
				writeJSON(a.io.Out, res)
				return nil
			}
			w := a.io.Out
			fmt.Fprintf(w, "%s Moved %s from %s to %s %s\n\n", a.paint("✓", green), res.Project, from, to,
				a.paint(fmt.Sprintf("(%s in %.0fs)", humanSize(res.SizeBytes), res.Seconds), dim))
			for _, ad := range res.Addresses {
				switch {
				case ad.Status == "none":
					fmt.Fprintf(w, "  %-10s %s\n", ad.App, a.paint("no release to bring (deploy it on "+to+")", dim))
				case ad.Status != "live":
					fmt.Fprintf(w, "  %-10s %s did not start on %s (tiffin deploys list %s %s)\n", ad.App, a.paint("✗", red), to, res.Project, ad.App)
				default:
					fmt.Fprintf(w, "  %-10s %s → %s\n", ad.App, orDefault(ad.Before, "-"), ad.After)
				}
			}
			for _, d := range res.Domains {
				switch {
				case d.Updated:
					fmt.Fprintf(w, "  %-10s %s now points at %s (%s set the records)\n", "domain", d.Domain, to, d.ManagedBy)
				default:
					var recs []string
					for _, r := range d.Records {
						recs = append(recs, r.String())
					}
					fmt.Fprintf(w, "  %-10s %s: point it at %s: %s\n", "domain", d.Domain, to, strings.Join(recs, "; "))
					if d.ManagedBy != "" {
						fmt.Fprintf(w, "  %-10s %s\n", "", a.paint(d.ManagedBy+" holds this zone: re-run with --update-dns to let "+to+" set them", dim))
					}
				}
			}
			if res.Import != nil {
				for _, n := range res.Import.Notes {
					fmt.Fprintf(w, "  %s %s\n", a.paint("·", dim), n)
				}
			}
			fmt.Fprintf(w, "\n%s %s\n", a.paint("→", amber), res.Next)
			if res.Import != nil && !res.Import.Healthy {
				a.code = ExitError
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the box to move it to (a box name from `tiffin up`)")
	cmd.Flags().BoolVar(&updateDNS, "update-dns", false, "let the new box set custom domains' DNS records, where its DNS provider holds the zone")
	return cmd
}
