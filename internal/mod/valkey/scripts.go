package valkey

import (
	"context"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// The script guard. Valkey runs one Lua script at a time and nothing else
// meanwhile, and never stops one on its own: a project's endless loop would
// hold every project's KV. Past scriptBusyMS the server answers other
// clients BUSY; the guard sees that and kills the script (SCRIPT KILL). A
// script that already wrote can't be killed (that would leave half its
// writes), so the guard stops the server without saving instead (SHUTDOWN
// NOSAVE): systemd starts it again from its append-only file, which has
// every write before the script and none of the script's own. Every
// project's connections drop for the few seconds that takes.

const (
	scriptBusyMS = 1000            // busy-reply-threshold in valkey.conf
	scriptCheck  = time.Second     // how often the guard looks
	scriptDial   = 2 * time.Second // per look
)

// guardScripts runs the script guard until ctx ends.
func guardScripts(ctx context.Context, p *platform.Platform, dial func(context.Context) (*Client, error)) {
	t := time.NewTicker(scriptCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		switch did := stopLongScript(ctx, dial); did {
		case "killed":
			p.Log.Warn("valkey: killed a Lua script that ran past its time", "limit_ms", scriptBusyMS)
		case "restarted":
			p.Log.Error("valkey: a Lua script that had written ran past its time and could not be killed; restarted the server without saving (its AOF has every write before the script)", "limit_ms", scriptBusyMS)
		}
	}
}

// stopLongScript stops a script that holds the server: "killed",
// "restarted", or "" when none does (or the server can't be reached).
func stopLongScript(ctx context.Context, dial func(context.Context) (*Client, error)) string {
	ctx, cancel := context.WithTimeout(ctx, scriptDial)
	defer cancel()
	c, err := dial(ctx)
	if err != nil {
		return ""
	}
	defer c.Close()
	if _, err := c.Do(ctx, "PING"); !replyIs(err, "BUSY") {
		return ""
	}
	_, err = c.Do(ctx, "SCRIPT", "KILL")
	if err == nil || replyIs(err, "NOTBUSY") {
		return "killed"
	}
	if !replyIs(err, "UNKILLABLE") {
		return ""
	}
	_, _ = c.Do(ctx, "SHUTDOWN", "NOSAVE") // the connection closes as the server exits
	return "restarted"
}

// replyIs reports whether err is a Valkey error reply with this code.
func replyIs(err error, code string) bool {
	re, ok := err.(RedisError)
	return ok && strings.HasPrefix(string(re), code)
}
