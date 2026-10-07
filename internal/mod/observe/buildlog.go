package observe

import (
	"bytes"
	"encoding/json"
	"time"
)

// BuildLine is one line of build output as the runtime writes it for the
// shipper: <AppLogsDir>/<project>/<app>/build/<deploy>.log, one JSON object
// per line. The deploy's build.log (what the deploy page reads) stays plain
// text; this copy adds the time of each line and the environment.
type BuildLine struct {
	Time string `json:"time"` // RFC 3339, when the line was written
	Env  string `json:"env"`  // prod or pr-<preview>, as app log lines name it
	Log  string `json:"log"`  // the line, without its newline
}

// buildLogRecord turns one line of a build file into a log record:
// source=build, labelled with the project, app, deploy and environment from
// the path and the line, and a level when the line says it failed or warns.
func buildLogRecord(line []byte, l AppLog) map[string]any {
	rec := map[string]any{"source": "build", "app": l.App, "project": l.Project, "deploy": l.Deploy}
	msg := string(line)
	var b BuildLine
	if t := bytes.TrimSpace(line); len(t) > 0 && t[0] == '{' && json.Unmarshal(t, &b) == nil {
		msg = b.Log
		if tm, err := time.Parse(time.RFC3339Nano, b.Time); err == nil {
			rec["_time"] = tm.UTC().Format(time.RFC3339Nano)
		}
		if b.Env != "" {
			rec["env"] = b.Env
		}
	}
	msg = stripANSI(msg)
	rec["_msg"] = msg
	if lv := inferLevel(msg); lv != "" {
		rec["level"] = lv
	}
	return rec
}
