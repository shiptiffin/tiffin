//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDockerfile builds an app from its Dockerfile on a fresh box (BuildKit's
// dockerfile frontend), through the CLI:
//
//	builder "dockerfile" with a build stage (target) → plain env reaches the
//	build as a build arg, a secret only as a secret mount → the app's command
//	replaces the image's CMD and listens on $PORT → it serves over HTTPS →
//	a folder with only a Dockerfile builds with it on its own (auto) → a
//	redeploy is zero-downtime like any other.
func TestDockerfile(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "dockerfile", "dock")
	phase("up", start)

	root := filepath.Join(b.dir, "dock")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The "run" stage's own CMD fails: the app's command must replace it.
	write("svc/docker/web.Dockerfile", `FROM busybox:1.37 AS base
ARG GREETING=none
RUN mkdir -p /www && echo "greeting:$GREETING" > /www/index.html
RUN --mount=type=secret,id=API_TOKEN,env=API_TOKEN if [ -n "$API_TOKEN" ]; then echo "secret:seen" >> /www/index.html; else echo "secret:missing" >> /www/index.html; fi
RUN echo "token-in-image:${API_TOKEN:-no}" >> /www/index.html

FROM base AS run
WORKDIR /www
CMD ["false"]

FROM base AS other
CMD ["sh", "-c", "echo wrong stage; sleep 3600"]
`)
	write("plain/Dockerfile", `FROM busybox:1.37
RUN mkdir -p /www && echo "auto-dockerfile" > /www/index.html
CMD ["sh", "-c", "exec httpd -f -p $PORT -h /www"]
`)
	write("tiffin.config.json", `{"project":"dock","apps":{
	  "svc":{"path":"svc","routes":["dock"],"builder":"dockerfile","dockerfile":"docker/web.Dockerfile","target":"run",
	         "command":"httpd -f -p $PORT -h /www","env":{"GREETING":"hello-from-env"}},
	  "plain":{"path":"plain","routes":["dock-plain"]}}}`)
	plan := b.ok("plan", root)
	hash, _ := plan["hash"].(string)
	b.ok("apply", root, "--confirm", hash, "-m", "e2e: dockerfile")
	b.waitReady("app/svc", "app/plain")
	const secret = "s3cret-token-e2e-9f2"
	b.ok("secrets", "set", "dock", "API_TOKEN", "--value", secret)
	c := b.https()

	// ---- builder "dockerfile": stage, build arg, secret mount, command ----
	p := time.Now()
	d1 := deployArgs(t, b, root, "--app", "svc")
	phase("dockerfile deploy", p)
	log := fmt.Sprint(b.ok("deploys", "build-log", "dock", "svc", d1.ID)["text"])
	t.Logf("build log (selected):\n%s", grepLines(log, "==> ", "FROM", "DONE", "ERROR"))
	for _, want := range []string{
		"==> builder: the Dockerfile docker/web.Dockerfile (builder \"dockerfile\")",
		"==> building docker/web.Dockerfile (stage run) with BuildKit",
		"build args (for the ARGs the Dockerfile declares): GREETING",
		"build secrets (RUN --mount=type=secret,id=NAME,env=NAME): API_TOKEN",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("build log lacks %q", want)
		}
	}
	if strings.Contains(log, secret) {
		t.Fatalf("the secret's value is in the build log")
	}
	dep := b.ok("deploys", "get", "dock", "svc", d1.ID)
	if dep["builder"] != "dockerfile" || dep["start"] != "httpd -f -p $PORT -h /www" {
		t.Fatalf("deploy record: builder %v, start %v", dep["builder"], dep["start"])
	}
	code, _, body := b.get(c, "GET", b.url("dock")+"/", nil)
	if code != 200 || !strings.Contains(body, "greeting:hello-from-env") || !strings.Contains(body, "secret:seen") || !strings.Contains(body, "token-in-image:no") {
		t.Fatalf("GET /: %d %q", code, body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("the secret reached the image")
	}
	// The image's history keeps no secret either.
	if hist := b.inBox("sudo /usr/local/bin/nerdctl --namespace tiffin image history --no-trunc " + fmt.Sprint(dep["image"]) + " 2>&1"); strings.Contains(hist, secret) {
		t.Fatalf("the secret is in the image history:\n%s", hist)
	}

	// ---- auto: a folder with a Dockerfile and nothing else ----
	p = time.Now()
	d2 := deployArgs(t, b, root, "--app", "plain")
	phase("auto dockerfile deploy", p)
	log = fmt.Sprint(b.ok("deploys", "build-log", "dock", "plain", d2.ID)["text"])
	if !strings.Contains(log, "==> builder: the Dockerfile Dockerfile (a Dockerfile and no package.json or Python project in the app's folder)") {
		t.Fatalf("auto-detection: %s", grepLines(log, "==> "))
	}
	if code, _, body := b.get(c, "GET", b.url("dock-plain")+"/", nil); code != 200 || strings.TrimSpace(body) != "auto-dockerfile" {
		t.Fatalf("GET plain: %d %q", code, body)
	}

	// ---- a redeploy of the live version (its source, built again) ----
	p = time.Now()
	rd := b.ok("deploys", "redeploy", "dock", "svc")
	d3 := waitDeploy(t, b, "dock", "svc", rd["id"].(string), 10*time.Minute)
	phase("redeploy", p)
	if d3["status"] != "live" || d3["trigger"] != "redeploy" {
		t.Fatalf("redeploy: %v", d3)
	}
	if code, _, body := b.get(c, "GET", b.url("dock")+"/", nil); code != 200 || !strings.Contains(body, "greeting:hello-from-env") {
		t.Fatalf("after the redeploy: %d %q", code, body)
	}
}
