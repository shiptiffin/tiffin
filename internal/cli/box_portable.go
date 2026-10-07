package cli

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/provider/lima"
	"github.com/spf13/cobra"
)

// The box key's place on every box (internal/install.Home + secrets.key).
const boxKeyPath = "/var/lib/tiffin/platform/secrets.key"

// boxExport mirrors the API's export record (the fields the CLI uses).
type boxExport struct {
	ID             string                   `json:"id"`
	Status         string                   `json:"status"`
	Phase          string                   `json:"phase"`
	Download       string                   `json:"download"`
	FileName       string                   `json:"fileName"`
	ContentBytes   int64                    `json:"contentBytes"`
	EstimatedBytes int64                    `json:"estimatedBytes"`
	SizeBytes      int64                    `json:"sizeBytes"`
	SHA256         string                   `json:"sha256"`
	DurationMs     int64                    `json:"durationMs"`
	WritesPausedMs int64                    `json:"writesPausedMs"`
	Parts          map[string]boxfile.Stats `json:"parts"`
	Projects       []string                 `json:"projects"`
	Key            struct {
		Included  bool   `json:"included"`
		Recipient string `json:"recipient"`
		Location  string `json:"location"`
		Note      string `json:"note"`
	} `json:"key"`
	Error string `json:"error"`
	Hint  string `json:"hint"`
}

// boxImport mirrors the API's import record.
type boxImport struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Phase        string `json:"phase"`
	Percent      int    `json:"percent"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
	SafetyBackup string `json:"safetyBackup"`
	Healthy      bool   `json:"healthy"`
	Failing      []struct {
		Name   string `json:"name"`
		Detail string `json:"detail"`
	} `json:"failing"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error"`
	Hint       string `json:"hint"`
}

func (a *app) boxCmd() *cobra.Command {
	box := &cobra.Command{Use: "box", Short: "This machine: what it uses, and moving it to another box (export, import)"}
	box.AddCommand(a.boxExportCmd(), a.boxImportCmd())
	return box
}

// remoteClient is the API client for commands that only make sense against
// a running box (not a local --home directory).
func (a *app) remoteClient(ctx context.Context, what string) (*client, error) {
	c, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	if c.handler != nil {
		c.close()
		return nil, &exitError{ExitInvalid, what + " works with a box: create one with `tiffin up`, or set TIFFIN_URL and TIFFIN_TOKEN"}
	}
	return c, nil
}

func (a *app) getJSON(ctx context.Context, c *client, path string, into any) error {
	status, raw, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err // an unreachableError: waits poll again (see patience)
	}
	if status != http.StatusOK {
		return problemOf(status, raw)
	}
	return json.Unmarshal(raw, into)
}

// finalRecord polls an export's record until it is done or failed: it is
// final a moment after the download ends. A poll that cannot reach the box
// is made again.
func finalRecord(ctx context.Context, c *client, record string, into any, status func() string) error {
	pt := patience{limit: time.Minute}
	for i := 0; i < 50 && status() != "done" && status() != "failed"; i++ {
		st, raw, err := c.do(ctx, http.MethodGet, record, nil, nil)
		switch {
		case err != nil:
			if !pt.again(err) {
				return err
			}
			if err := sleepCtx(ctx, time.Second); err != nil {
				return err
			}
			continue
		case st != http.StatusOK:
			return problemOf(st, raw)
		}
		pt.again(nil)
		if err := json.Unmarshal(raw, into); err != nil {
			return err
		}
		if status() != "done" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return nil
}

// progressLine redraws one status line on stderr for people; agents get
// nothing until the JSON result.
type progressLine struct {
	stop chan struct{}
	done chan struct{}
}

func (a *app) startProgress(render func() string) *progressLine {
	pl := &progressLine{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(pl.done)
		if !a.tty() {
			<-pl.stop
			return
		}
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-pl.stop:
				fmt.Fprint(a.io.Err, "\r\033[K")
				return
			case <-t.C:
				fmt.Fprintf(a.io.Err, "\r\033[K%s %s", a.paint("·", dim), render())
			}
		}
	}()
	return pl
}

func (pl *progressLine) end() {
	close(pl.stop)
	<-pl.done
}

func (a *app) boxExportCmd() *cobra.Command {
	var includeKey, withHistory, store bool
	var keyOut string
	cmd := &cobra.Command{
		Use:   "export [file]",
		Short: "Export the whole box to one file",
		Long: "Writes one archive (.tiffin) with everything needed to recreate this box elsewhere: platform state (projects, " +
			"changes, tokens, people, passkeys, settings, deploys; secrets stay encrypted), every Postgres database, Valkey, buckets " +
			"and objects, email, analytics, the live deploys' app images and static sites, git repositories and the HTTPS certificate " +
			"authority. App writes pause for a moment while a consistent snapshot is taken; the archive streams to your computer, so " +
			"the box needs no extra disk. Ends with the archive's size and SHA-256.\n\n" +
			"The archive holds every project's data. Secrets inside stay encrypted to the box key, which is left out unless you pass " +
			"--include-key; then keep the key yourself (--key-out saves it from a local box) because `tiffin box import` needs it.",
		Example: "  tiffin box export\n  tiffin box export shop.tiffin --key-out shop.key\n  tiffin box export shop.tiffin --include-key",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start := time.Now()
			c, err := a.remoteClient(ctx, "box export")
			if err != nil {
				return err
			}
			defer c.close()
			status, raw, err := c.doOnce(ctx, http.MethodPost, "/v1/box/exports", nil, map[string]any{"includeKey": includeKey, "withHistory": withHistory, "store": store})
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status != http.StatusOK {
				return problemOf(status, raw)
			}
			var ex boxExport
			if err := json.Unmarshal(raw, &ex); err != nil {
				return err
			}
			path := first(args)
			if path == "" {
				path = ex.FileName
			}
			if _, err := os.Stat(path); err == nil {
				return &exitError{ExitInvalid, path + " already exists; pass another file name"}
			}
			if store {
				// The box writes the archive first; wait for it.
				pl := a.startProgress(func() string { return "the box is writing the archive: " + orDefault(ex.Phase, ex.Status) })
				pt := patience{limit: 2 * time.Minute}
				for ex.Status == "pending" || ex.Status == "running" {
					time.Sleep(time.Second)
					if err := a.getJSON(ctx, c, "/v1/box/exports/"+ex.ID, &ex); err != nil {
						if pt.again(err) {
							continue
						}
						pl.end()
						return err
					}
					pt.again(nil)
				}
				pl.end()
				if ex.Status != "done" {
					return &exitError{ExitError, "export " + ex.ID + " " + ex.Status + ": " + ex.Error + hintOf(ex.Hint)}
				}
			}
			n, sum, err := a.downloadExport(ctx, c, &ex, path)
			if err != nil {
				return err
			}
			// The record is final a moment after the stream ends.
			if err := finalRecord(ctx, c, "/v1/box/exports/"+ex.ID, &ex, func() string { return ex.Status }); err != nil {
				return err
			}
			if ex.Status != "done" {
				_ = os.Remove(path)
				return &exitError{ExitError, "export " + ex.ID + " " + ex.Status + ": " + ex.Error + hintOf(ex.Hint)}
			}
			if ex.SHA256 != sum || ex.SizeBytes != n {
				_ = os.Remove(path)
				return &exitError{ExitError, fmt.Sprintf("the download does not match what the box wrote (%d bytes, sha256 %s; the box says %d, %s); try again", n, sum, ex.SizeBytes, ex.SHA256)}
			}
			if store {
				// It is on this computer now: free the box's disk.
				_, _, _ = c.do(ctx, http.MethodDelete, "/v1/box/exports/"+ex.ID, nil, nil)
			}
			keyFile := ""
			if keyOut != "" {
				if keyFile, err = a.saveBoxKey(ctx, keyOut); err != nil {
					return err
				}
			}
			secs := time.Since(start).Seconds()
			if !a.tty() {
				out := map[string]any{"export": ex.ID, "file": path, "sizeBytes": n, "sha256": sum, "seconds": round1s(secs),
					"writesPausedMs": ex.WritesPausedMs, "projects": ex.Projects, "parts": ex.Parts, "key": ex.Key}
				if keyFile != "" {
					out["keyFile"] = keyFile
				}
				writeJSON(a.io.Out, out)
				return nil
			}
			w := a.io.Out
			fmt.Fprintf(w, "%s Exported the box to %s %s\n\n", a.paint("✓", green), a.paint(path, bold),
				a.paint(fmt.Sprintf("(%s in %s; writes paused %d ms)", humanSize(n), time.Since(start).Round(100*time.Millisecond), ex.WritesPausedMs), dim))
			fmt.Fprintf(w, "  %-9s %s\n", "SHA-256", sum)
			fmt.Fprintf(w, "  %-9s %s\n", "Projects", orDefault(strings.Join(ex.Projects, ", "), "none"))
			fmt.Fprintf(w, "  %-9s %s\n", "Contains", describeParts(ex.Parts))
			switch {
			case ex.Key.Included:
				fmt.Fprintf(w, "\n%s The box key is inside: anyone with this file can read every secret. Keep it as safe as the box.\n", a.paint("!", amber))
			case keyFile != "":
				fmt.Fprintf(w, "  %-9s %s %s\n", "Key", keyFile, a.paint("(import needs it: tiffin box import "+path+" --key-file "+keyFile+")", dim))
			default:
				fmt.Fprintf(w, "\n%s The box key is NOT in the archive, and import needs it. It is %s on the box (recipient %s).\n",
					a.paint("!", amber), boxKeyPath, ex.Key.Recipient)
				fmt.Fprintf(w, "  Save it before you delete this box: %s\n", a.paint(a.keyHint(path), bold))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&includeKey, "include-key", false, "put the box key (decrypts every secret) inside the archive")
	f.BoolVar(&withHistory, "with-history", false, "also export logs, metrics, build logs and database snapshots")
	f.BoolVar(&store, "store", false, "let the box write the archive to its own disk first, then download it")
	f.StringVar(&keyOut, "key-out", "", "save the box key to this file (local boxes; the file is created 0600)")
	return cmd
}

func hintOf(h string) string {
	if h == "" {
		return ""
	}
	return "\n  hint: " + h
}

func round1s(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func describeParts(parts map[string]boxfile.Stats) string {
	var out []string
	if s, ok := parts["postgres"]; ok {
		out = append(out, fmt.Sprintf("%d database(s)", s.Items))
	}
	if s, ok := parts["valkey"]; ok {
		out = append(out, fmt.Sprintf("%d Valkey key(s)", s.Items))
	}
	if s, ok := parts["storage"]; ok {
		out = append(out, fmt.Sprintf("storage (%d files)", s.Files))
	}
	if s, ok := parts["images"]; ok {
		out = append(out, fmt.Sprintf("%d app image(s)", s.Items))
	}
	if s, ok := parts["runtime-static"]; ok && s.Files > 0 {
		out = append(out, "static sites")
	}
	for _, n := range []string{"email", "analytics", "edge"} {
		if s, ok := parts[n]; ok && s.Files > 0 {
			out = append(out, n)
		}
	}
	return strings.Join(out, ", ")
}

// keyHint is how to copy the box key off the box.
func (a *app) keyHint(archive string) string {
	keyFile := strings.TrimSuffix(filepath.Base(archive), boxfile.FileExt) + ".key"
	if _, bx := a.currentBox(); bx != nil && bx.Provider == "local" && a.url == "" {
		return "limactl shell " + lima.New().Instance + " sudo cat " + boxKeyPath + " > " + keyFile + "  (or pass --key-out next time)"
	}
	return "ssh root@<box> cat " + boxKeyPath + " > " + keyFile
}

// saveBoxKey copies the box key of a local (Lima) box to path.
func (a *app) saveBoxKey(ctx context.Context, path string) (string, error) {
	if _, bx := a.currentBox(); bx == nil || bx.Provider != "local" || a.url != "" {
		return "", &exitError{ExitInvalid, "--key-out works with a local box; on a server copy " + boxKeyPath + " yourself (as root)"}
	}
	out, err := exec.CommandContext(ctx, "limactl", "shell", "--workdir", "/", lima.New().Instance, "--", "sudo", "cat", boxKeyPath).Output()
	if err != nil {
		return "", &exitError{ExitError, "read the box key: " + err.Error()}
	}
	key := strings.TrimSpace(string(out))
	if _, err := age.ParseX25519Identity(key); err != nil {
		return "", &exitError{ExitError, "the box key is not an age key: " + err.Error()}
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// downloadExport streams the archive into path (via path.part), hashing as
// it goes, and shows progress with the box's current phase.
func (a *app) downloadExport(ctx context.Context, c *client, ex *boxExport, path string) (int64, string, error) {
	return a.downloadArchive(ctx, c, ex.Download, "/v1/box/exports/"+ex.ID, path)
}

// downloadArchive streams an export's download into path; record is the
// export's record (its phase and error).
func (a *app) downloadArchive(ctx context.Context, c *client, download, record, path string) (int64, string, error) {
	res, err := c.stream(ctx, http.MethodGet, download, nil, "", "application/octet-stream")
	if err != nil {
		return 0, "", &exitError{ExitError, err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return 0, "", problemOf(res.StatusCode, raw)
	}
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", &exitError{ExitInvalid, err.Error()}
	}
	h := sha256.New()
	var mu sync.Mutex
	var n int64
	phase := "starting"
	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go func() {
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			var cur boxExport
			if a.getJSON(pollCtx, c, record, &cur) == nil {
				mu.Lock()
				phase = cur.Phase
				mu.Unlock()
			}
		}
	}()
	started := time.Now()
	pl := a.startProgress(func() string {
		mu.Lock()
		defer mu.Unlock()
		rate := float64(n) / max(0.001, time.Since(started).Seconds())
		return fmt.Sprintf("%s received (%s/s) · %s", humanSize(n), humanSize(int64(rate)), phase)
	})
	buf := make([]byte, 1<<20)
	var werr error
	for {
		k, rerr := res.Body.Read(buf)
		if k > 0 {
			if _, err := f.Write(buf[:k]); err != nil {
				werr = err
				break
			}
			h.Write(buf[:k])
			mu.Lock()
			n += int64(k)
			mu.Unlock()
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			werr = fmt.Errorf("the download broke off after %s: %v", humanSize(n), rerr)
			break
		}
	}
	pl.end()
	stopPoll()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(part)
		var cur boxExport
		if a.getJSON(ctx, c, record, &cur) == nil && cur.Error != "" {
			werr = fmt.Errorf("%v (the box says: %s)", werr, cur.Error)
		}
		return 0, "", &exitError{ExitError, werr.Error()}
	}
	if err := os.Rename(part, path); err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func (a *app) boxImportCmd() *cobra.Command {
	var keyFile, confirm string
	var replace bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import a box export onto this box",
		Long: "Uploads an archive made by `tiffin box export`, verifies it on the box, restores everything (databases, Valkey, " +
			"files, app images, platform state, people and tokens), restarts the box's service once to swap in the state, and waits " +
			"until every project has converged and its apps answer. Ends with the box's status.\n\n" +
			"A fresh box (no projects) takes the import as is. A box with projects is refused unless you pass --replace together " +
			"with --confirm <box name>: everything on it is replaced (a full backup of it is taken first). Archives from a newer " +
			"Tiffin are refused: update this box first. If the archive does not carry the box key, pass it with --key-file.\n\n" +
			"Tokens of the source box keep working here, and so does this box's owner token. The HTTPS certificate authority " +
			"becomes the source box's: the CLI updates its copy, browsers may need `tiffin trust` again.",
		Example: "  tiffin box import shop.tiffin --key-file shop.key\n  tiffin box import shop.tiffin --replace --confirm local",
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
			if man.Kind == boxfile.ProjectKind {
				return &exitError{ExitInvalid, path + " is an export of project " + man.Project + ", not of a whole box: import it with `tiffin projects import`"}
			}
			if man.Format > boxfile.FormatVersion {
				return &exitError{ExitInvalid, fmt.Sprintf("%s was made by a newer Tiffin (archive format %d); update this CLI and the box first", path, man.Format)}
			}
			key := ""
			if keyFile != "" {
				raw, err := os.ReadFile(keyFile)
				if err != nil {
					return &exitError{ExitInvalid, "--key-file: " + err.Error()}
				}
				key = strings.TrimSpace(string(raw))
				id, err := age.ParseX25519Identity(key)
				if err != nil {
					return &exitError{ExitInvalid, "--key-file: not an age secret key: " + err.Error()}
				}
				if !man.IncludesKey && id.Recipient().String() != man.Recipient {
					return &exitError{ExitInvalid, fmt.Sprintf("--key-file: that key (%s) is not the one this archive's secrets are encrypted to (%s)", id.Recipient(), man.Recipient)}
				}
			} else if !man.IncludesKey {
				return &exitError{ExitInvalid, fmt.Sprintf("%s does not carry the box key that decrypts its secrets; pass it with --key-file "+
					"(the source box's %s, recipient %s)", path, boxKeyPath, man.Recipient)}
			}

			c, err := a.remoteClient(ctx, "box import")
			if err != nil {
				return err
			}
			defer c.close()
			boxName, bx := a.currentBox()
			if a.url != "" {
				if u, err := url.Parse(a.url); err == nil {
					boxName = u.Hostname()
				}
				bx = nil
			}
			var projects []struct {
				Name string `json:"name"`
			}
			if err := a.getJSON(ctx, c, "/v1/projects", &projects); err != nil {
				return err
			}
			if len(projects) > 0 {
				var names []string
				for _, p := range projects {
					names = append(names, p.Name)
				}
				detail := fmt.Sprintf("this box (%s) already has %d project(s): %s; importing replaces everything on it", boxName, len(names), strings.Join(names, ", "))
				switch {
				case !replace:
					return a.needConfirm(detail, "re-run with --replace --confirm "+boxName+" (a full backup of this box is taken first)")
				case confirm != boxName:
					return a.needConfirm(detail, "re-run with --confirm "+boxName)
				}
			}

			a.say("Archive %s: %s from %s (Tiffin %s, %s), %d project(s): %s", filepath.Base(path), humanSize(fi.Size()),
				orDefault(man.Source.Domain, "?"), man.TiffinVersion, man.CreatedAt.Format(time.RFC3339), len(man.Projects), strings.Join(man.Projects, ", "))
			im, err := a.uploadImport(ctx, c, f, fi.Size())
			if err != nil {
				return err
			}
			uploaded := time.Now()

			body := map[string]any{"replace": replace}
			if key != "" && !man.IncludesKey {
				body["secretsKey"] = key
			}
			status, raw, err := c.do(ctx, http.MethodPost, "/v1/box/imports/"+im.ID+"/apply", nil, body)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status == http.StatusPreconditionRequired {
				// The preview: this command's own checks above are the confirmation.
				var cp struct {
					Confirm string `json:"confirm"`
				}
				_ = json.Unmarshal(raw, &cp)
				body["confirm"] = cp.Confirm
				status, raw, err = c.do(ctx, http.MethodPost, "/v1/box/imports/"+im.ID+"/apply", nil, body)
				if err != nil {
					return &exitError{ExitError, err.Error()}
				}
			}
			if status != http.StatusAccepted && status != http.StatusOK {
				return problemOf(status, raw)
			}

			// The box restarts with the source box's HTTPS CA: trust both until it is done.
			pc, err := a.importClient(c, bx, man.CAPEM)
			if err != nil {
				return err
			}
			var restored time.Time
			last := ""
			var imMu sync.Mutex
			pl := a.startProgress(func() string {
				imMu.Lock()
				defer imMu.Unlock()
				return fmt.Sprintf("%s (%d%%)", orDefault(im.Phase, im.Status), im.Percent)
			})
			unreachableSince := time.Time{}
			for im.Status != "done" && im.Status != "failed" {
				time.Sleep(time.Second)
				var cur boxImport
				if err := a.getJSON(ctx, pc, "/v1/box/imports/"+im.ID, &cur); err != nil {
					// The service restarts in the middle: wait for it.
					if unreachableSince.IsZero() {
						unreachableSince = time.Now()
					}
					if time.Since(unreachableSince) > 5*time.Minute {
						pl.end()
						return &exitError{ExitError, "the box has not answered for 5 minutes during the import: " + err.Error()}
					}
					continue
				}
				unreachableSince = time.Time{}
				if cur.Status != last && (cur.Status == "restarting" || cur.Status == "converging") && restored.IsZero() {
					restored = time.Now()
				}
				last = cur.Status
				imMu.Lock()
				*im = cur
				imMu.Unlock()
			}
			pl.end()
			if im.Status == "failed" {
				return &exitError{ExitError, "import " + im.ID + " failed: " + im.Error + hintOf(im.Hint)}
			}
			// The CLI's copy of the box's CA, and an agent token that exists here.
			if bx != nil && bx.CAFile != "" && man.CAPEM != "" {
				if err := os.WriteFile(bx.CAFile, []byte(man.CAPEM), 0o644); err != nil {
					return err
				}
			}
			if bx != nil {
				if err := a.refreshAgentToken(ctx, boxName); err != nil {
					a.say("could not mint a new agent token: %v", err)
				}
			}
			var st map[string]any
			_ = a.getJSON(ctx, pc, "/v1/status", &st)
			if restored.IsZero() {
				restored = time.Now()
			}
			timing := map[string]float64{"upload": round1s(uploaded.Sub(start).Seconds()), "restore": round1s(restored.Sub(uploaded).Seconds()),
				"converge": round1s(time.Since(restored).Seconds()), "total": round1s(time.Since(start).Seconds())}
			if !a.tty() {
				writeJSON(a.io.Out, map[string]any{"import": im.ID, "file": path, "sizeBytes": im.SizeBytes, "sha256": im.SHA256,
					"projects": man.Projects, "healthy": im.Healthy, "failing": im.Failing, "safetyBackup": im.SafetyBackup,
					"seconds": timing, "status": st})
			} else {
				w := a.io.Out
				mark, word := a.paint("✓", green), "Imported"
				if !im.Healthy {
					mark, word = a.paint("!", amber), "Imported, but not everything is green"
				}
				fmt.Fprintf(w, "%s %s %d project(s) into %s %s\n", mark, word, len(man.Projects), boxName,
					a.paint(fmt.Sprintf("(%s: upload %.0fs, restore %.0fs, converge %.0fs)", time.Since(start).Round(time.Second), timing["upload"], timing["restore"], timing["converge"]), dim))
				for _, fc := range im.Failing {
					fmt.Fprintf(w, "  %s %s: %s\n", a.paint("✗", red), fc.Name, fc.Detail)
				}
				if im.SafetyBackup != "" {
					fmt.Fprintf(w, "  %-9s %s\n", "Backup", im.SafetyBackup+" (this box before the import)")
				}
				fmt.Fprintf(w, "  %-9s run %s in browsers that trusted the old certificate authority\n", "HTTPS", a.paint("tiffin trust", bold))
			}
			if !im.Healthy {
				a.code = ExitError
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&keyFile, "key-file", "", "the source box's key (its "+boxKeyPath+"), when the archive does not include it")
	f.BoolVar(&replace, "replace", false, "replace a box that already has projects (needs --confirm <box name>)")
	f.StringVar(&confirm, "confirm", "", "the box name, to confirm --replace (e.g. local)")
	return cmd
}

// needConfirm reports a refused irreversible step the way `down` does.
func (a *app) needConfirm(detail, hint string) error {
	if a.tty() {
		fmt.Fprintf(a.io.Out, "%s %s\n%s %s\n", a.paint("irreversible:", red), detail, a.paint("→", amber), hint)
	} else {
		writeJSON(a.io.Out, map[string]any{"code": "confirm_required", "status": 428, "detail": detail, "hint": hint})
	}
	a.code = ExitConfirm
	return nil
}

// uploadImport streams the archive to the box, which verifies it on arrival.
func (a *app) uploadImport(ctx context.Context, c *client, f *os.File, size int64) (*boxImport, error) {
	var im boxImport
	if err := a.uploadArchive(ctx, c, "/v1/box/imports", f, size, &im); err != nil {
		return nil, err
	}
	a.say("Uploaded and verified %s (sha256 %s)", humanSize(im.SizeBytes), im.SHA256)
	return &im, nil
}

// uploadArchive streams an archive file to path on the box (which verifies
// it on arrival) and decodes the answer into into. The upload is sent again
// when the connection drops before the box got it, and its Idempotency-Key
// finds it when the connection drops after: it is never stored twice.
func (a *app) uploadArchive(ctx context.Context, c *client, path string, f *os.File, size int64, into any) error {
	var mu sync.Mutex
	var sent int64
	body := fileBody(f, size, func(n int64) {
		mu.Lock()
		if n < 0 {
			sent = 0 // sent again from the start
		} else {
			sent += n
		}
		mu.Unlock()
	})
	body.key = newIdempotencyKey()
	pl := a.startProgress(func() string {
		mu.Lock()
		defer mu.Unlock()
		if sent >= size {
			return "uploaded; the box is verifying it"
		}
		return fmt.Sprintf("uploading %s of %s", humanSize(sent), humanSize(size))
	})
	res, err := c.stream(ctx, http.MethodPost, path, body, "application/octet-stream", "")
	pl.end()
	if err != nil {
		return &exitError{ExitError, "upload: " + err.Error()}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return problemOf(res.StatusCode, raw)
	}
	return json.Unmarshal(raw, into)
}

type countingReader struct {
	r io.Reader
	f func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.f(int64(n))
	return n, err
}

// importClient is c with a transport that trusts the archive's CA too (the
// box serves it after the restart).
func (a *app) importClient(c *client, bx *boxConfig, caPEM string) (*client, error) {
	if bx == nil || caPEM == "" {
		return c, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if pem, err := os.ReadFile(bx.CAFile); err == nil {
		pool.AppendCertsFromPEM(pem)
	}
	pool.AppendCertsFromPEM([]byte(caPEM))
	tr, err := boxTransport(bx.CAFile)
	if err != nil {
		return nil, err
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	cp := *c
	cp.transport = tr
	return &cp, nil
}

// refreshAgentToken mints a new agent key if the box's changed tokens no
// longer know the CLI's (an import brings the source box's tokens), or if
// the one it has is narrower than full access to all projects.
func (a *app) refreshAgentToken(ctx context.Context, name string) error {
	f, err := a.loadBoxes()
	if err != nil {
		return err
	}
	bx := f.Boxes[name]
	if bx == nil {
		return nil
	}
	c, err := a.boxClient(bx, bx.Token)
	if err != nil {
		return err
	}
	changed, err := ensureAgentKey(ctx, a, c, bx)
	if err != nil || !changed {
		return err
	}
	return a.saveBoxes(f)
}
