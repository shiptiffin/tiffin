package queue

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/btahir/tiffin/internal/state"
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

// CheckKey reports whether key is project's current key, or an app's key
// derived from it, and which project and app it belongs to ("" for the
// project's own key).
func CheckKey(ctx context.Context, k Keys, key string) (project, app string, ok bool) {
	project, app = ProjectOfKey(key)
	if project == "" {
		return "", "", false
	}
	want, _, err := k.Get(ctx, project)
	if err != nil {
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

func (k *kvKeys) Get(ctx context.Context, project string) (string, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	raw, ok, err := k.db.KVGet(ctx, "queue", "keys/"+project)
	if err != nil {
		return "", "", err
	}
	var pk projectKeys
	if ok && json.Unmarshal(raw, &pk) == nil && pk.AppKey != "" {
		return pk.AppKey, pk.Signing, nil
	}
	pk = newProjectKeys(project)
	b, _ := json.Marshal(pk)
	if err := k.db.KVPut(ctx, "queue", "keys/"+project, b); err != nil {
		return "", "", err
	}
	return pk.AppKey, pk.Signing, nil
}
