package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// MaxJSONValues is the most values (array items and object members) one
// request body may hold. huma validates every value and keeps every error,
// so without a bound a few megabytes of [{},{},...] become millions of
// field errors before any handler runs. The largest legitimate bodies (5,000
// table-editor row changes, 50,000 object keys) stay well below it.
const MaxJSONValues = 200_000

// maxProblemErrors is the most field errors a problem lists.
const maxProblemErrors = 50

// jsonFormat decodes request bodies keeping numbers exact (json.Number, not
// float64: 9007199254740993 stays itself on its way to Postgres or a job's
// payload) and refuses bodies with more than MaxJSONValues values before
// anything is decoded.
var jsonFormat = huma.Format{
	Marshal: huma.DefaultJSONFormat.Marshal,
	Unmarshal: func(data []byte, v any) error {
		if n := jsonValues(data, MaxJSONValues); n > MaxJSONValues {
			return fmt.Errorf("the request body has more than %d values (array items and object members); send it in smaller requests", MaxJSONValues)
		}
		return DecodeJSON(data, v)
	},
}

// DecodeJSON is json.Unmarshal with numbers kept as json.Number when they
// land in an interface (any, []any, map[string]any).
func DecodeJSON(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON: more after the top-level value")
	}
	return nil
}

// jsonValues counts the values in a JSON text (one per array item or object
// member, plus each array and object), stopping once it passes limit. It
// does not validate: the decoder does that.
func jsonValues(data []byte, limit int) int {
	n, inStr, esc := 0, false, false
	for _, c := range data {
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == ',' || c == '[' || c == '{':
			if n++; n > limit {
				return n
			}
		}
	}
	return n
}

// projectAccess refuses, before the request body is read, a caller that
// cannot reach the project the path names: every operation under
// /v1/projects/{project} needs at least read access to it, and parsing and
// validating a body for a key that may not touch the project is wasted work
// (or an attack's lever).
func (a *API) projectAccess(ctx huma.Context, next func(huma.Context)) {
	p := PrincipalFrom(ctx.Context())
	if p == nil || !opHasProject(ctx.Operation()) {
		next(ctx)
		return
	}
	if err := p.Require(tokens.ScopeRead, ctx.Param("project")); err != nil {
		pr, _ := toProblem(err).(*Problem)
		if pr == nil {
			pr = problem(403, "forbidden", err.Error())
		}
		writeProblem(ctx, pr, nil)
		return
	}
	next(ctx)
}

func opHasProject(o *huma.Operation) bool {
	for _, p := range o.Parameters {
		if p.In == "path" && p.Name == "project" {
			return true
		}
	}
	return false
}

// Raw uploads (huma reads no body for operations without a Body field, and
// sets no deadline on it) must keep moving: at least uploadMinBytes every
// uploadWindow. A client that stalls is cut off instead of holding the
// handler, its file and its connection forever.
var (
	uploadWindow         = 30 * time.Second // a var for tests
	uploadMinBytes int64 = 64 << 10
)

// UploadBody is the request body of a raw upload, with a moving read
// deadline: each uploadWindow must bring at least uploadMinBytes. The
// deadline is cleared when the body ends, so work after the upload (and the
// server's own read of the next request) is not cut short.
func UploadBody(ctx huma.Context) io.Reader {
	r := ctx.BodyReader()
	if r == nil {
		return nil
	}
	return &uploadBody{ctx: ctx, r: r}
}

type uploadBody struct {
	ctx   huma.Context
	r     io.Reader
	n     int64 // bytes read in the current window
	armed bool
	done  bool
}

func (u *uploadBody) Read(p []byte) (int, error) {
	if u.done {
		return u.r.Read(p)
	}
	if !u.armed || u.n >= uploadMinBytes {
		u.armed, u.n = true, 0
		_ = u.ctx.SetReadDeadline(time.Now().Add(uploadWindow))
	}
	n, err := u.r.Read(p)
	u.n += int64(n)
	if err != nil {
		u.done = true
		_ = u.ctx.SetReadDeadline(time.Time{})
	}
	return n, err
}
