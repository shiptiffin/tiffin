// Package sentry reads error events sent with the Sentry protocol (envelope
// and legacy store endpoints), so the official Sentry SDKs (@sentry/bun,
// @sentry/browser, ...) report into Tiffin unchanged, and groups them into
// issues by fingerprint. Reference: https://develop.sentry.dev/sdk/envelopes/
package sentry

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Event is the part of a Sentry event Tiffin keeps.
type Event struct {
	EventID     string            `json:"event_id"`
	Timestamp   time.Time         `json:"timestamp"`
	Level       string            `json:"level"`
	Platform    string            `json:"platform,omitempty"`
	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	ServerName  string            `json:"server_name,omitempty"`
	Message     string            `json:"message,omitempty"`
	Exceptions  []Exception       `json:"exceptions,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	URL         string            `json:"url,omitempty"`
	Transaction string            `json:"transaction,omitempty"`
	Fingerprint []string          `json:"fingerprint,omitempty"`
}

// Exception is one exception in a chain (innermost last, as Sentry sends them).
type Exception struct {
	Type   string  `json:"type"`
	Value  string  `json:"value"`
	Frames []Frame `json:"frames,omitempty"`
}

// Frame is one stack frame (oldest first, as Sentry sends them).
type Frame struct {
	Filename string `json:"filename,omitempty"`
	Function string `json:"function,omitempty"`
	Module   string `json:"module,omitempty"`
	Lineno   int    `json:"lineno,omitempty"`
	Colno    int    `json:"colno,omitempty"`
	InApp    *bool  `json:"in_app,omitempty"`
	Context  string `json:"context_line,omitempty"`
}

// rawEvent mirrors the wire format loosely: several fields have more than
// one accepted shape.
type rawEvent struct {
	EventID     string          `json:"event_id"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Level       string          `json:"level"`
	Platform    string          `json:"platform"`
	Release     string          `json:"release"`
	Environment string          `json:"environment"`
	ServerName  string          `json:"server_name"`
	Message     json.RawMessage `json:"message"`
	LogEntry    *struct {
		Formatted string `json:"formatted"`
		Message   string `json:"message"`
	} `json:"logentry"`
	Exception json.RawMessage `json:"exception"`
	Tags      json.RawMessage `json:"tags"`
	Request   *struct {
		URL string `json:"url"`
	} `json:"request"`
	Transaction string   `json:"transaction"`
	Fingerprint []string `json:"fingerprint"`
}

type rawException struct {
	Type       string `json:"type"`
	Value      string `json:"value"`
	Module     string `json:"module"`
	Stacktrace *struct {
		Frames []Frame `json:"frames"`
	} `json:"stacktrace"`
}

// ParseEvent decodes one event payload.
func ParseEvent(b []byte) (*Event, error) {
	var r rawEvent
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("event is not JSON: %w", err)
	}
	e := &Event{EventID: strings.ReplaceAll(r.EventID, "-", ""), Level: r.Level, Platform: r.Platform, Release: r.Release,
		Environment: r.Environment, ServerName: r.ServerName, Transaction: r.Transaction, Fingerprint: r.Fingerprint}
	if e.Level == "" {
		e.Level = "error"
	}
	e.Timestamp = parseTime(r.Timestamp)
	// message: a string or {"formatted": ..., "message": ...}
	if len(r.Message) > 0 {
		var s string
		if json.Unmarshal(r.Message, &s) == nil {
			e.Message = s
		} else {
			var m struct{ Formatted, Message string }
			if json.Unmarshal(r.Message, &m) == nil {
				e.Message = first(m.Formatted, m.Message)
			}
		}
	}
	if e.Message == "" && r.LogEntry != nil {
		e.Message = first(r.LogEntry.Formatted, r.LogEntry.Message)
	}
	// exception: {"values": [...]} or a bare list
	if len(r.Exception) > 0 {
		var vals []rawException
		var wrapped struct {
			Values []rawException `json:"values"`
		}
		if json.Unmarshal(r.Exception, &wrapped) == nil && wrapped.Values != nil {
			vals = wrapped.Values
		} else {
			_ = json.Unmarshal(r.Exception, &vals)
		}
		for _, v := range vals {
			ex := Exception{Type: v.Type, Value: v.Value}
			if ex.Type == "" && v.Module != "" {
				ex.Type = v.Module
			}
			if v.Stacktrace != nil {
				ex.Frames = v.Stacktrace.Frames
				if len(ex.Frames) > 100 {
					ex.Frames = ex.Frames[len(ex.Frames)-100:]
				}
			}
			e.Exceptions = append(e.Exceptions, ex)
		}
	}
	// tags: {"k": "v"} or [["k", "v"]]
	if len(r.Tags) > 0 {
		e.Tags = map[string]string{}
		var m map[string]any
		if json.Unmarshal(r.Tags, &m) == nil {
			for k, v := range m {
				e.Tags[k] = fmt.Sprint(v)
			}
		} else {
			var pairs [][]string
			if json.Unmarshal(r.Tags, &pairs) == nil {
				for _, p := range pairs {
					if len(p) == 2 {
						e.Tags[p[0]] = p[1]
					}
				}
			}
		}
	}
	if r.Request != nil {
		e.URL = r.Request.URL
	}
	if e.Message == "" && len(e.Exceptions) == 0 {
		return nil, errors.New("event has neither a message nor an exception")
	}
	return e, nil
}

func parseTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Now().UTC()
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil && f > 0 {
		sec := int64(f)
		return time.Unix(sec, int64((f-float64(sec))*1e9)).UTC()
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Now().UTC()
}

func first(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// Envelope is a parsed envelope: its header and the event items in it.
type Envelope struct {
	DSN     string
	EventID string
	Events  [][]byte // payloads of "event" items
	Skipped []string // item types we do not store (transactions, sessions, ...)
}

// ParseEnvelope reads an envelope body (already decompressed).
func ParseEnvelope(r io.Reader) (*Envelope, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, 20<<20), 64<<10)
	head, err := readLine(br)
	if err != nil {
		return nil, fmt.Errorf("envelope header: %w", err)
	}
	var h struct {
		DSN     string `json:"dsn"`
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(head, &h); err != nil {
		return nil, fmt.Errorf("envelope header is not JSON: %w", err)
	}
	env := &Envelope{DSN: h.DSN, EventID: strings.ReplaceAll(h.EventID, "-", "")}
	for {
		line, err := readLine(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ih struct {
			Type   string `json:"type"`
			Length *int   `json:"length"`
		}
		if err := json.Unmarshal(line, &ih); err != nil {
			return nil, fmt.Errorf("envelope item header is not JSON: %w", err)
		}
		var payload []byte
		if ih.Length != nil {
			if *ih.Length < 0 || *ih.Length > 20<<20 {
				return nil, errors.New("envelope item length out of range")
			}
			payload = make([]byte, *ih.Length)
			if _, err := io.ReadFull(br, payload); err != nil {
				return nil, fmt.Errorf("envelope item: %w", err)
			}
			// Optional newline after the payload.
			if b, err := br.Peek(1); err == nil && b[0] == '\n' {
				_, _ = br.ReadByte()
			}
		} else {
			payload, err = readLine(br)
			if err != nil && err != io.EOF {
				return nil, err
			}
		}
		if ih.Type == "event" {
			env.Events = append(env.Events, payload)
		} else {
			env.Skipped = append(env.Skipped, ih.Type)
		}
	}
	return env, nil
}

func readLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if err == io.EOF && len(line) > 0 {
		return bytes.TrimRight(line, "\r\n"), nil
	}
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(line, "\r\n"), nil
}

// AuthKey extracts the public key from an X-Sentry-Auth header
// ("Sentry sentry_key=abc, sentry_version=7"), a sentry_key query value or a DSN.
func AuthKey(header, query, dsn string) string {
	if header != "" {
		h := strings.TrimSpace(header)
		h = strings.TrimPrefix(h, "Sentry ")
		for _, part := range strings.Split(h, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && k == "sentry_key" {
				return v
			}
		}
	}
	if query != "" {
		return query
	}
	if dsn != "" {
		if _, rest, ok := strings.Cut(dsn, "://"); ok {
			if user, _, ok := strings.Cut(rest, "@"); ok {
				k, _, _ := strings.Cut(user, ":")
				return k
			}
		}
	}
	return ""
}

var (
	reHex    = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	reNum    = regexp.MustCompile(`\d+`)
	reQuoted = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	reURL    = regexp.MustCompile(`https?://\S+`)
)

// normalizeMessage strips the variable parts of a message (ids, numbers,
// quoted values, URLs) so "user 42 not found" and "user 7 not found" group.
func normalizeMessage(s string) string {
	s = reURL.ReplaceAllString(s, "<url>")
	s = reQuoted.ReplaceAllString(s, "<str>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reNum.ReplaceAllString(s, "<n>")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// Fingerprint returns the grouping hash for an event. Rules, in order:
//  1. an explicit SDK fingerprint ("{{ default }}" expands to rule 2/3);
//  2. the innermost exception's type plus its in-app frames (file and
//     function, no line numbers so small edits keep the group);
//  3. exception type plus normalised value when there is no stack;
//  4. the normalised message.
func Fingerprint(e *Event) string {
	def := defaultParts(e)
	var parts []string
	if len(e.Fingerprint) > 0 {
		for _, f := range e.Fingerprint {
			if f == "{{ default }}" || f == "{{default}}" {
				parts = append(parts, def...)
			} else {
				parts = append(parts, f)
			}
		}
	} else {
		parts = def
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

func defaultParts(e *Event) []string {
	if n := len(e.Exceptions); n > 0 {
		ex := e.Exceptions[n-1]
		parts := []string{"type:" + ex.Type}
		frames := inAppFrames(ex.Frames)
		if len(frames) == 0 {
			return append(parts, "value:"+normalizeMessage(ex.Value))
		}
		for _, f := range frames {
			parts = append(parts, "frame:"+frameKey(f))
		}
		return parts
	}
	return []string{"message:" + normalizeMessage(e.Message)}
}

func inAppFrames(frames []Frame) []Frame {
	var out []Frame
	for _, f := range frames {
		if f.InApp != nil && *f.InApp {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		// SDKs that don't mark in_app: skip obvious dependency frames.
		for _, f := range frames {
			if !strings.Contains(f.Filename, "node_modules") && !strings.HasPrefix(f.Filename, "node:") && !strings.HasPrefix(f.Module, "node:") {
				out = append(out, f)
			}
		}
	}
	if len(out) > 30 {
		out = out[len(out)-30:]
	}
	return out
}

var reBuildHash = regexp.MustCompile(`[.-][0-9a-f]{6,}(\.js|\.mjs|\.cjs)$`)

func frameKey(f Frame) string {
	file := f.Module
	if file == "" {
		file = f.Filename
		// Drop origins and build hashes: app-3f9a1c.js and app-77b2e0.js are the same file.
		if i := strings.Index(file, "://"); i >= 0 {
			if j := strings.IndexByte(file[i+3:], '/'); j >= 0 {
				file = file[i+3+j:]
			}
		}
		file = reBuildHash.ReplaceAllString(file, "$1")
	}
	return file + ":" + f.Function
}

// Title is the issue title: "TypeError: x is undefined" or the message.
func Title(e *Event) string {
	if n := len(e.Exceptions); n > 0 {
		ex := e.Exceptions[n-1]
		t := ex.Type
		if ex.Value != "" {
			if t != "" {
				t += ": "
			}
			t += ex.Value
		}
		return clip(first(t, "Error"), 300)
	}
	return clip(first(e.Message, "Error"), 300)
}

// Culprit is where it happened: the innermost in-app frame.
func Culprit(e *Event) string {
	if e.Transaction != "" {
		return clip(e.Transaction, 200)
	}
	if n := len(e.Exceptions); n > 0 {
		fs := inAppFrames(e.Exceptions[n-1].Frames)
		if len(fs) > 0 {
			f := fs[len(fs)-1]
			loc := first(f.Module, f.Filename)
			if f.Lineno > 0 {
				loc += ":" + strconv.Itoa(f.Lineno)
			}
			if f.Function != "" && f.Function != "?" && f.Function != "<anonymous>" {
				loc = f.Function + " (" + loc + ")"
			}
			return clip(loc, 200)
		}
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
