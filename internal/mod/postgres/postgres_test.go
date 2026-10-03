package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestConfig(t *testing.T) {
	c := Config(3000)
	for _, want := range []string{
		"file_copy_method = clone",
		"listen_addresses = '127.0.0.1'",
		"unix_socket_directories = '/var/run/postgresql'",
		"shared_buffers = 375MB",
		"archive_command = 'pgbackrest --stanza=tiffin archive-push %p'",
		"shared_preload_libraries = 'pg_cron,pg_stat_statements'",
		"data_directory = '" + DataDir + "'",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("config lacks %q", want)
		}
	}
	if !strings.Contains(Config(1024), "shared_buffers = 128MB") || !strings.Contains(Config(64000), "shared_buffers = 4096MB") {
		t.Error("shared_buffers must be clamped to 128MB..4GB")
	}
	if strings.Contains(hba, "trust") || !strings.Contains(hba, "local   all             postgres                                peer map=tiffin") {
		t.Error("pg_hba must never trust, and the box connects by peer through the tiffin map")
	}
}

func TestNames(t *testing.T) {
	long := "a" + strings.Repeat("b-", 19) + "c" // 40 chars, the longest slug
	if len(long) != 40 {
		t.Fatal(len(long))
	}
	if db := BranchDatabase(long, "a"+strings.Repeat("x", 18)); len(db) > 63 {
		t.Fatalf("branch database %q is %d bytes, over Postgres's 63", db, len(db))
	}
	if Database("my-shop") != "p_my_shop" || BranchDatabase("my-shop", "pr-12") != "p_my_shop__pr_12" {
		t.Error("names")
	}
	for _, ok := range []string{"pr-1", "a", "feature-x"} {
		if !BranchPattern.MatchString(ok) {
			t.Errorf("%q should be a valid branch", ok)
		}
	}
	for _, bad := range []string{"", "1abc", "PR", "a_b", strings.Repeat("a", 20), "a;drop"} {
		if BranchPattern.MatchString(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
	if quoteIdent(`we"ird`) != `"we""ird"` || quoteLiteral("it's") != "'it''s'" {
		t.Error("quoting")
	}
	if extName("pgvector") != "vector" || extName("uuid-ossp") != "uuid_ossp" || extName("pg_trgm") != "pg_trgm" {
		t.Error("extension aliases")
	}
}

func TestMeta(t *testing.T) {
	m := dbMeta{Tiffin: "branch", Project: "shop", Branch: "pr-1", From: "main"}
	raw, _ := json.Marshal(m)
	s := string(raw)
	got, ok := parseMeta(&s)
	if !ok || got.Branch != "pr-1" || got.Project != "shop" {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
	junk := "a comment someone wrote"
	if _, ok := parseMeta(&junk); ok {
		t.Error("free-text comments are not Tiffin metadata")
	}
	if _, ok := parseMeta(nil); ok {
		t.Error("nil comment")
	}
}

func TestTextValue(t *testing.T) {
	f := func(oid uint32) pgconn.FieldDescription { return pgconn.FieldDescription{DataTypeOID: oid} }
	cases := []struct {
		oid  uint32
		in   string
		want string
	}{
		{pgtype.Int4OID, "42", "42"},
		{pgtype.Int8OID, "9007199254740993", `"9007199254740993"`}, // beyond 2^53 stays text
		{pgtype.Int8OID, "-7", "-7"},
		{pgtype.Float8OID, "1.5", "1.5"},
		{pgtype.Float8OID, "NaN", `"NaN"`},
		{pgtype.NumericOID, "1.50", `"1.50"`}, // exact decimals stay text
		{pgtype.BoolOID, "t", "true"},
		{pgtype.JSONBOID, `{"a": [1]}`, `{"a":[1]}`},
		{pgtype.TextOID, "héllo", `"héllo"`},
	}
	for _, c := range cases {
		b, _ := json.Marshal(textValue(f(c.oid), []byte(c.in)))
		if string(b) != c.want {
			t.Errorf("oid %d %q → %s, want %s", c.oid, c.in, b, c.want)
		}
	}
	if textValue(f(pgtype.TextOID), nil) != nil {
		t.Error("NULL must be nil")
	}
}

func TestEncodeParams(t *testing.T) {
	got, err := encodeParams([]any{"a", 3.0, 2.5, true, nil, map[string]any{"k": 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "3", "2.5", "true", "", `{"k":1}`}
	for i, w := range want {
		if string(got[i]) != w {
			t.Errorf("param %d = %q, want %q", i, got[i], w)
		}
	}
	if got[4] != nil {
		t.Error("null must be sent as SQL NULL")
	}
}

func TestSQLErrorHints(t *testing.T) {
	err := sqlError(&pgconn.PgError{Code: "25006", Message: "cannot execute INSERT in a read-only transaction"})
	b, _ := json.Marshal(err)
	if !strings.Contains(string(b), "write=true") || !strings.Contains(string(b), `"status":422`) {
		t.Fatalf("read-only violation must point at write=true: %s", b)
	}
}

func TestEnvAndBranchEnv(t *testing.T) {
	home := t.TempDir()
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sec, err := platform.OpenSecrets(db, home)
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Secrets: sec, Home: home, Log: slog.Default()}
	ctx := context.Background()
	m := &Module{}
	env, err := m.Env(ctx, p, "my-shop", "web")
	if err != nil || env != nil {
		t.Fatalf("no postgres service → no env, got %v %v", env, err)
	}
	if err := db.Commit(ctx, &change.Change{ID: "chg_01J00000000000000000000000", Project: "my-shop", Version: 1,
		Plan: change.Plan{Project: "my-shop", Ops: []change.Op{{Action: change.Create, Address: "service/postgres", After: json.RawMessage(`{}`)}}}}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	env, err = m.Env(ctx, p, "my-shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(env["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	if u.Scheme != "postgresql" || u.Host != "127.0.0.1:5432" || u.Path != "/p_my_shop" || u.User.Username() != "p_my_shop" || pw != env["PGPASSWORD"] || len(pw) != 32 {
		t.Fatalf("DATABASE_URL %s", env["DATABASE_URL"])
	}
	if env["PGHOST"] != "127.0.0.1" || env["PGDATABASE"] != "p_my_shop" || env["PGUSER"] != "p_my_shop" || env["PGPORT"] != "5432" {
		t.Fatalf("PG* vars: %v", env)
	}
	b, err := BranchEnv(ctx, p, "my-shop", "pr-7")
	if err != nil || !strings.Contains(b["DATABASE_URL"], "/p_my_shop__pr_7?") || b["PGPASSWORD"] != pw {
		t.Fatalf("branch env: %v %v", b, err)
	}
	if _, err := BranchEnv(ctx, p, "my-shop", "Bad_Name"); err == nil {
		t.Fatal("invalid branch accepted")
	}
	s, _ := ConnEnv(ctx, p, "my-shop", "", true)
	if s["PGHOST"] != SocketDir || !strings.Contains(s["DATABASE_URL"], "host=%2Fvar%2Frun%2Fpostgresql") && !strings.Contains(s["DATABASE_URL"], "host=/var/run/postgresql") {
		t.Fatalf("socket env: %v", s)
	}
}
