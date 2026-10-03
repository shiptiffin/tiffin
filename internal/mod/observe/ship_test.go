package observe

import (
	"strings"
	"testing"
)

func TestShipParsing(t *testing.T) {
	rec, cur, ok := journalRecord([]byte(`{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"1727900000000000","PRIORITY":"6","_SYSTEMD_UNIT":"tiffin.service","MESSAGE":"{\"time\":\"x\",\"level\":\"ERROR\",\"msg\":\"reconcile\",\"project\":\"shop\"}"}`))
	if !ok || cur != "c1" || rec["_msg"] != "reconcile" || rec["level"] != "error" || rec["unit"] != "tiffin.service" || rec["f.project"] != "shop" || rec["_time"] != "2024-10-02T20:13:20Z" {
		t.Fatalf("%v %v", rec, cur)
	}
	if rec, _, ok := journalRecord([]byte(`{"MESSAGE":[104,105,255],"_SYSTEMD_UNIT":"x.service"}`)); !ok || rec["_msg"] != "hi?" {
		t.Fatalf("byte-array message: %v", rec)
	}
	p, a, d, ok := appLogLabels("/l/apps", "/l/apps/shop/web/dpl_1.log")
	if !ok || p != "shop" || a != "web" || d != "dpl_1" {
		t.Fatal(p, a, d)
	}
	if _, _, _, ok := appLogLabels("/l/apps", "/l/apps/x.log"); ok {
		t.Fatal("too shallow")
	}
	r := appLogRecord([]byte(`{"log":"{\"level\":\"warn\",\"msg\":\"slow query\",\"ms\":812,\"app\":\"evil\"}\n","stream":"stderr","time":"2026-10-02T10:00:00.5Z"}`), "web", "dpl_1")
	if r["_msg"] != "slow query" || r["level"] != "warn" || r["ms"] != "812" || r["app"] != "web" || r["stream"] != "stderr" || r["_time"] != "2026-10-02T10:00:00.5Z" {
		t.Fatalf("container json-file line: %v", r)
	}
	if r := appLogRecord([]byte("plain text line"), "web", ""); r["_msg"] != "plain text line" {
		t.Fatal(r)
	}
	if got := Redact("Owner token: tfn_gfaarbwkgglmgwng7euk67xsrl354z2o7o77buzl and code tfl_o4vl3qaftm5223daasuv2he2kd4mt4xl"); strings.Contains(got, "gfaar") || strings.Contains(got, "o4vl3") {
		t.Fatal(got)
	}
}
