package queue

import (
	"context"
	"crypto/subtle"
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

// ProjectOfKey returns the project an app key claims (checked by CheckKey).
func ProjectOfKey(key string) string {
	if !strings.HasPrefix(key, "tqk_") {
		return ""
	}
	rest := key[len("tqk_"):]
	i := strings.LastIndex(rest, "_")
	if i <= 0 {
		return ""
	}
	return rest[:i]
}

// CheckKey reports whether key is project's current app key.
func CheckKey(ctx context.Context, k Keys, key string) (string, bool) {
	project := ProjectOfKey(key)
	if project == "" {
		return "", false
	}
	want, _, err := k.Get(ctx, project)
	if err != nil || subtle.ConstantTimeCompare([]byte(want), []byte(key)) != 1 {
		return "", false
	}
	return project, true
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
