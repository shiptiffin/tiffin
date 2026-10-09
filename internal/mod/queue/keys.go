package queue

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	"github.com/shiptiffin/tiffin/internal/state"
)

type projectKeys struct {
	AppKey  string `json:"appKey"`
	Signing string `json:"signing"`
}

func newProjectKeys(project string) projectKeys {
	return projectKeys{AppKey: "tqk_" + project + "_" + randomHex(24), Signing: "tqs_" + randomHex(32)}
}

// AppKey is the key one app of a project gets (TIFFIN_QUEUE_KEY): the
// project's key bound to the app's name, so the box knows which app sent a
// job. "tqk_<project>_<app>_<mac>"; project and app names have no "_".
func AppKey(projectKey, project, app string) string {
	return "tqk_" + project + "_" + app + "_" + appMAC(projectKey, app)
}

func appMAC(projectKey, app string) string {
	m := hmac.New(sha256.New, []byte(projectKey))
	m.Write([]byte("app:" + app))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// ProjectOfKey returns the project (and, for an app's key, the app) a key
// claims (checked by CheckKey).
func ProjectOfKey(key string) (project, app string) {
	rest, ok := strings.CutPrefix(key, "tqk_")
	if !ok {
		return "", ""
	}
	switch parts := strings.Split(rest, "_"); len(parts) {
	case 2: // the project's own key
		return parts[0], ""
	case 3:
		return parts[0], parts[1]
	}
	return "", ""
}

// projectSlug is the shape of a project name (the API's path pattern).
var projectSlug = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// CheckKey reports whether key is project's current key, or an app's key
// derived from it, and which project and app it belongs to ("" for the
// project's own key). It only reads: a key for a project that has none
// (never had apps, or was deleted) is wrong, and creates nothing.
func CheckKey(ctx context.Context, k Keys, key string) (project, app string, ok bool) {
	project, app = ProjectOfKey(key)
	if !projectSlug.MatchString(project) {
		return "", "", false
	}
	want, _, found, err := k.Lookup(ctx, project)
	if err != nil || !found {
		return "", "", false
	}
	if app != "" {
		want = AppKey(want, project, app)
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(key)) != 1 {
		return "", "", false
	}
	return project, app, true
}

// memKeys keeps keys in memory (tests and spec builds).
type memKeys struct {
	mu sync.Mutex
	m  map[string]projectKeys
}

func newMemKeys() *memKeys { return &memKeys{m: map[string]projectKeys{}} }

func (k *memKeys) Lookup(_ context.Context, project string) (string, string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	pk, ok := k.m[project]
	return pk.AppKey, pk.Signing, ok, nil
}

func (k *memKeys) Delete(_ context.Context, project string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, project)
	return nil
}

func (k *memKeys) Get(_ context.Context, project string) (string, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	pk, ok := k.m[project]
	if !ok {
		pk = newProjectKeys(project)
		k.m[project] = pk
	}
	return pk.AppKey, pk.Signing, nil
}

// kvKeys keeps keys in the platform state database (root-only, on the box).
type kvKeys struct {
	db *state.DB
	mu sync.Mutex
}

func (k *kvKeys) Lookup(ctx context.Context, project string) (string, string, bool, error) {
	raw, ok, err := k.db.KVGet(ctx, "queue", "keys/"+project)
	if err != nil || !ok {
		return "", "", false, err
	}
	var pk projectKeys
	if json.Unmarshal(raw, &pk) != nil || pk.AppKey == "" {
		return "", "", false, nil
	}
	return pk.AppKey, pk.Signing, true, nil
}

func (k *kvKeys) Delete(ctx context.Context, project string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.db.KVDelete(ctx, "queue", "keys/"+project)
}

func (k *kvKeys) Get(ctx context.Context, project string) (string, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if a, s, ok, err := k.Lookup(ctx, project); err != nil || ok {
		return a, s, err
	}
	pk := newProjectKeys(project)
	b, _ := json.Marshal(pk)
	if err := k.db.KVPut(ctx, "queue", "keys/"+project, b); err != nil {
		return "", "", err
	}
	return pk.AppKey, pk.Signing, nil
}
