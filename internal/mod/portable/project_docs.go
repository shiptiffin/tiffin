package portable

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/manifest"
)

// Credentials of the services docker-compose.yml starts: local only, and
// meant to be changed.
const (
	composePGUser   = "app"
	composePGPass   = "app"
	composeS3Key    = "tiffin"
	composeS3Secret = "change-me-please"
)

// databaseSetup is database-setup.sql: what database.sql expects to exist
// (the box makes it for every project database).
func databaseSetup(pg *PostgresInfo) string {
	var b strings.Builder
	b.WriteString("-- Run before database.sql: the extensions the database used, and Tiffin's helpers for\n")
	b.WriteString("-- row-level security (SET LOCAL app.org_id = '...' / app.user_id). The official Postgres\n")
	b.WriteString("-- image runs it first (docker-compose.yml mounts it as 00-setup.sql).\n")
	for _, e := range pg.Extensions {
		fmt.Fprintf(&b, "CREATE EXTENSION IF NOT EXISTS %s CASCADE;\n", qi(e))
	}
	b.WriteString(`CREATE SCHEMA IF NOT EXISTS tiffin;
CREATE OR REPLACE FUNCTION tiffin.org_id() RETURNS text LANGUAGE sql STABLE PARALLEL SAFE
  AS $$ SELECT nullif(current_setting('app.org_id', true), '') $$;
CREATE OR REPLACE FUNCTION tiffin.user_id() RETURNS text LANGUAGE sql STABLE PARALLEL SAFE
  AS $$ SELECT nullif(current_setting('app.user_id', true), '') $$;
GRANT USAGE ON SCHEMA tiffin TO PUBLIC;
`)
	return b.String()
}

// yq quotes a value for docker-compose.yml (a YAML double-quoted string,
// with $ escaped from compose's interpolation).
func yq(s string) string { return strings.ReplaceAll(strconv.Quote(s), "$", "$$") }

// appEnv is the env an app gets under docker compose: the same names the
// box sets (DATABASE_URL, REDIS_URL, S3_*...), pointing at the compose
// services, then the project's and the app's own env.
func appEnv(info *ProjectInfo, m *manifest.Manifest, app string, port int) map[string]string {
	env := map[string]string{"TIFFIN_PROJECT": info.Project, "TIFFIN_APP": app}
	if port > 0 {
		env["PORT"] = strconv.Itoa(port)
	}
	if info.Postgres != nil || m.Services.Postgres != nil {
		env["DATABASE_URL"] = "postgresql://" + composePGUser + ":" + composePGPass + "@postgres:5432/app?sslmode=disable"
		env["PGHOST"], env["PGPORT"], env["PGUSER"], env["PGPASSWORD"], env["PGDATABASE"] = "postgres", "5432", composePGUser, composePGPass, "app"
	}
	if info.Valkey != nil {
		env["REDIS_URL"], env["VALKEY_URL"], env["VALKEY_PREFIX"] = "redis://valkey:6379", "redis://valkey:6379", info.Valkey.Prefix
	}
	if len(info.Buckets) > 0 {
		for k, v := range map[string]string{"S3_ENDPOINT": "http://minio:9000", "S3_REGION": "us-east-1", "S3_ACCESS_KEY_ID": composeS3Key,
			"S3_SECRET_ACCESS_KEY": composeS3Secret, "AWS_ENDPOINT_URL": "http://minio:9000", "AWS_REGION": "us-east-1",
			"AWS_ACCESS_KEY_ID": composeS3Key, "AWS_SECRET_ACCESS_KEY": composeS3Secret, "AWS_S3_FORCE_PATH_STYLE": "true"} {
			env[k] = v
		}
		for _, bk := range info.Buckets {
			env["S3_BUCKET_"+strings.ToUpper(strings.ReplaceAll(bk.Name, "-", "_"))] = bk.S3Name
		}
		if len(info.Buckets) == 1 {
			env["S3_BUCKET"] = info.Buckets[0].S3Name
		}
	}
	for k, v := range m.Env {
		env[k] = v
	}
	for k, v := range m.Apps[app].Env {
		env[k] = v
	}
	return env
}

// compose is docker-compose.yml: the project without Tiffin.
func compose(info *ProjectInfo, m *manifest.Manifest) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("# Project %s without Tiffin. Read README.md first: container apps need `docker load -i apps/<app>/image.tar`.", info.Project)
	w("name: %s", info.Project)
	w("services:")
	var deps []string
	if info.Postgres != nil {
		image := "postgres:18"
		if slices.Contains(info.Postgres.Extensions, "vector") {
			image = "pgvector/pgvector:pg18"
		}
		w("  postgres:")
		w("    image: %s", image)
		w("    environment:")
		w("      POSTGRES_USER: %s", yq(composePGUser))
		w("      POSTGRES_PASSWORD: %s", yq(composePGPass))
		w("      POSTGRES_DB: \"app\"")
		w("    volumes:")
		w("      - ./database-setup.sql:/docker-entrypoint-initdb.d/00-setup.sql:ro")
		w("      - ./database.sql:/docker-entrypoint-initdb.d/01-database.sql:ro")
		w("      - postgres:/var/lib/postgresql")
		w("    ports: [\"5432:5432\"]")
		w("    healthcheck:")
		w("      test: [\"CMD\", \"pg_isready\", \"-U\", %s]", yq(composePGUser))
		w("      interval: 2s")
		w("      retries: 60")
		deps = append(deps, "postgres")
	}
	if info.Valkey != nil {
		w("  valkey:")
		w("    image: valkey/valkey:8")
		w("    ports: [\"6379:6379\"]")
		deps = append(deps, "valkey")
	}
	if len(info.Buckets) > 0 {
		w("  minio:")
		w("    image: minio/minio")
		w("    command: [\"server\", \"/data\", \"--console-address\", \":9001\"]")
		w("    environment:")
		w("      MINIO_ROOT_USER: %s", yq(composeS3Key))
		w("      MINIO_ROOT_PASSWORD: %s", yq(composeS3Secret))
		w("    ports: [\"9000:9000\", \"9001:9001\"]")
		w("    volumes:")
		w("      - minio:/data")
		var load []string
		load = append(load, "mc alias set local http://minio:9000 "+composeS3Key+" "+composeS3Secret)
		for _, bk := range info.Buckets {
			load = append(load, "mc mb -p local/"+bk.S3Name, "mc mirror --overwrite /files/"+bk.Name+" local/"+bk.S3Name)
			if bk.Public {
				load = append(load, "mc anonymous set download local/"+bk.S3Name)
			}
		}
		w("  minio-load: # copies files/ into the buckets once")
		w("    image: minio/mc")
		w("    depends_on: [minio]")
		w("    entrypoint: [\"/bin/sh\", \"-c\"]")
		w("    command: [%s]", yq("until mc alias set local http://minio:9000 "+composeS3Key+" "+composeS3Secret+" >/dev/null 2>&1; do sleep 1; done && "+strings.Join(load[1:], " && ")))
		w("    volumes:")
		w("      - ./files:/files:ro")
		deps = append(deps, "minio")
	}
	port := 3000
	for _, a := range info.Apps {
		spec := m.Apps[a.Name]
		switch {
		case a.Site:
			w("  %s: # static site", a.Name)
			w("    image: nginx:alpine")
			w("    volumes:")
			w("      - ./apps/%s/site:/usr/share/nginx/html:ro", a.Name)
			w("    ports: [\"%d:80\"]", port)
			port++
			continue
		case a.Image == "":
			w("  # %s: no release was exported (it was never deployed)", a.Name)
			continue
		}
		w("  %s:", a.Name)
		if a.ImageFile {
			w("    image: %s # docker load -i apps/%s/image.tar", a.Image, a.Name)
		} else {
			w("    image: %s", a.Image)
		}
		listen := 0
		if spec.Role != manifest.RoleWorker {
			listen = 3000
		}
		env := appEnv(info, m, a.Name, listen)
		w("    environment:")
		for _, k := range sortedKeys(env) {
			w("      %s: %s", k, yq(env[k]))
		}
		if info.Secrets.Plain {
			w("    env_file: [.env]")
		} else if len(info.Secrets.Names) > 0 {
			w("    # secrets to set: %s", strings.Join(info.Secrets.Names, ", "))
		}
		if a.Disk {
			w("    volumes: # its disk folders (the box builds apps in /app)")
			for _, p := range spec.Disk {
				w("      - ./disk/%s/%s:/app/%s", a.Name, p, p)
			}
		}
		if listen > 0 {
			w("    ports: [\"%d:%d\"]", port, listen)
			port++
		}
		if len(deps) > 0 {
			w("    depends_on: [%s]", strings.Join(deps, ", "))
		}
	}
	if info.Postgres != nil || len(info.Buckets) > 0 {
		w("volumes:")
		if info.Postgres != nil {
			w("  postgres:")
		}
		if len(info.Buckets) > 0 {
			w("  minio:")
		}
	}
	return b.String()
}

// readme is README.md: what the archive holds and how to use it.
func readme(info *ProjectInfo) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("# %s", info.Project)
	w("")
	w("An export of the Tiffin project **%s** from %s, made %s with Tiffin %s.", info.Project, orDash(info.Source.Domain),
		info.ExportedAt.Format("2006-01-02 15:04 UTC"), orDash(info.TiffinVersion))
	w("Everything in it is an ordinary file: you can run the project without Tiffin, or import it into another Tiffin box.")
	w("")
	w("## What is inside")
	w("")
	w("| File | What it is |")
	w("|---|---|")
	w("| `project.json` | The project in machine-readable form: config, apps, buckets, secret names |")
	w("| `tiffin.config.ts` | The project's config |")
	w("| `docker-compose.yml` | Runs it with Docker (see below) |")
	if info.History {
		w("| `history/changes.jsonl` | The project's History: every change, oldest first |")
	}
	switch {
	case info.Secrets.Plain:
		w("| `.env` | Secrets in **plain text** (%s). Keep this archive private |", strings.Join(info.Secrets.Names, ", "))
	case len(info.Secrets.Names) > 0:
		w("| `secrets.json` | Secrets (%s), sealed to the source box's key; not readable without it |", strings.Join(info.Secrets.Names, ", "))
	}
	if pg := info.Postgres; pg != nil {
		w("| `database-setup.sql` | Extensions and Tiffin's helper functions; run it before `database.sql` |")
		w("| `database.sql` | The database (`pg_dump`, plain SQL, no owners or grants): `psql -f database.sql` |")
	}
	if info.Valkey != nil {
		w("| `cache.jsonl` | The cache: one key per line (prefix `%s` left out), its TTL and its `DUMP` payload for `RESTORE` |", info.Valkey.Prefix)
	}
	for _, bk := range info.Buckets {
		w("| `files/%s/` | Every object of bucket %s, one file each (content types in `user.*` extended attributes) |", bk.Name, bk.Name)
	}
	if info.Git {
		w("| `source.git/` | The push-to-deploy git repository: `git clone source.git` |")
	}
	for _, a := range info.Apps {
		switch {
		case a.Site:
			w("| `apps/%s/site/` | The static site %s served |", a.Name, a.Name)
		case a.ImageFile:
			w("| `apps/%s/image.tar` | App %s's image (%s): `docker load -i apps/%s/image.tar` |", a.Name, a.Name, a.Image, a.Name)
		}
	}
	w("")
	w("## Run it without Tiffin")
	w("")
	w("You need Docker with Compose.")
	w("")
	step := 1
	for _, a := range info.Apps {
		if a.ImageFile {
			w("%d. `docker load -i apps/%s/image.tar`", step, a.Name)
			step++
		}
	}
	if len(info.Secrets.Names) > 0 && !info.Secrets.Plain {
		w("%d. Set the secrets (%s) in each app's `environment` in `docker-compose.yml` (this archive holds them sealed).", step, strings.Join(info.Secrets.Names, ", "))
		step++
	}
	w("%d. `docker compose up`", step)
	w("")
	w("Postgres loads `database-setup.sql` and `database.sql` the first time it starts (an empty volume). ")
	if len(info.Buckets) > 0 {
		w("MinIO stands in for the box's S3 storage; `minio-load` copies `files/` into buckets of the same names once. ")
	}
	w("Apps get the same environment variables as on the box (`DATABASE_URL`, `REDIS_URL`, `S3_*`...), pointing at those services, " +
		"and listen on `PORT`; web apps are published on localhost from port 3000 up.")
	w("")
	w("Not covered by the compose file:")
	w("")
	w("- Sign-in (Tiffin auth): the users are in the database's `auth` schema, but the auth endpoint (`TIFFIN_AUTH_URL`) is the box's.")
	w("- Email: set `SMTP_URL` to a mail relay of your own.")
	if info.Postgres != nil && len(info.Postgres.Cron) > 0 {
		w("- pg_cron jobs (in `project.json`): the official image has no pg_cron.")
	}
	if info.Valkey != nil {
		w("- The cache starts empty; load `cache.jsonl` with `RESTORE <prefix><key> <ttlMs> <dump>` if you need it.")
	}
	w("")
	w("## Import it into Tiffin")
	w("")
	w("```")
	w("tiffin projects import %s.tiffin                # as project %s", info.Project, info.Project)
	w("tiffin projects import %s.tiffin --name %s-2    # beside it, under another name", info.Project, info.Project)
	w("```")
	w("")
	w("An import makes a new project and never replaces one. Under another name, the apps get that project's own addresses; custom domains stay with the original.")
	if len(info.Secrets.Names) > 0 && !info.Secrets.Plain {
		w("Sealed secrets need the source box's key on any other box: `--secrets-key-file`, or import without them and set them again.")
	}
	return b.String()
}
