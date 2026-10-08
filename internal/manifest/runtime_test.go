package manifest

import (
	"strings"
	"testing"
)

func TestRuntime(t *testing.T) {
	m, err := Parse([]byte(`{"project":"shop","apps":{"web":{"framework":"next","runtime":"node"},"api":{"runtime":"bun"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Apps["web"].Runtime != RuntimeNode {
		t.Fatalf("runtime: %q", m.Apps["web"].Runtime)
	}
	got := string(RenderConfig(m, ""))
	if !strings.Contains(got, `runtime: "node"`) || strings.Contains(got, `runtime: "bun"`) {
		t.Fatalf("node is written, the bun default is left out:\n%s", got)
	}
	for fw, want := range map[string]string{"static": "no runtime", "hono": "@hono/node-server"} {
		_, err := Parse([]byte(`{"project":"shop","apps":{"web":{"framework":"` + fw + `","runtime":"node"}}}`))
		if err == nil || !strings.Contains(err.Error(), "/apps/web/runtime") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s on node: %v", fw, err)
		}
	}
	if _, err := Parse([]byte(`{"project":"shop","apps":{"web":{"runtime":"deno"}}}`)); err == nil {
		t.Error("an unknown runtime should be refused")
	}
}

// Every project has Database, KV, Files (with the private bucket "files"),
// Email and Analytics, whatever its config says; their settings still come
// from the config.
func TestAlwaysOn(t *testing.T) {
	m, err := Parse([]byte(`{"project":"shop"}`))
	if err != nil {
		t.Fatal(err)
	}
	s := m.Services
	if s.Postgres == nil || s.Valkey == nil || s.Storage == nil || s.Email == nil || s.Analytics == nil {
		t.Fatalf("services = %+v", s)
	}
	if b, ok := s.Storage.Buckets[DefaultBucket]; !ok || b.Public {
		t.Fatalf("buckets = %+v", s.Storage.Buckets)
	}
	if s.Auth != nil {
		t.Fatal("auth stays optional")
	}
	if s.Valkey.MaxMemoryMB != DefaultValkeyMemMB || s.Analytics.RetentionDays != DefaultAnalyticsRetentionDays {
		t.Fatalf("defaults: %+v %+v", s.Valkey, s.Analytics)
	}
	m, err = Parse([]byte(`{"project":"shop","services":{"postgres":{"extensions":["vector"]},"valkey":{"maxMemoryMB":128},
		"storage":{"buckets":{"media":{"public":true},"files":{"public":true}}},"analytics":{"retentionDays":30}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s = m.Services
	if len(s.Postgres.Extensions) != 1 || s.Valkey.MaxMemoryMB != 128 || s.Analytics.RetentionDays != 30 || s.Email == nil {
		t.Fatalf("options from the config: %+v", s)
	}
	if !s.Storage.Buckets["files"].Public || !s.Storage.Buckets["media"].Public || len(s.Storage.Buckets) != 2 {
		t.Fatalf("declared buckets win: %+v", s.Storage.Buckets)
	}
	// Leaving them out renders a config without them, and it means the same.
	if src := string(RenderConfig(Normalize(&Manifest{Project: "shop"}), "")); strings.Contains(src, "services") {
		t.Fatalf("the always-on parts with no settings are left out:\n%s", src)
	}
}
