package edge

import (
	"reflect"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
)

// TestOwnModulesReturnPointers: Caddy decodes a module's JSON into what its
// New returns and warns, on every config load, when that is not a pointer.
func TestOwnModulesReturnPointers(t *testing.T) {
	n := 0
	for _, id := range caddy.Modules() {
		info, err := caddy.GetModule(id)
		if err != nil {
			t.Fatal(err)
		}
		v := info.New()
		typ := reflect.TypeOf(v)
		pkg := typ.PkgPath()
		if typ.Kind() == reflect.Pointer {
			pkg = typ.Elem().PkgPath()
		}
		if !strings.HasPrefix(pkg, "github.com/shiptiffin/tiffin/") {
			continue
		}
		n++
		if typ.Kind() != reflect.Pointer {
			t.Errorf("module %s: New returns a %s, not a pointer", id, typ)
		}
	}
	if n == 0 {
		t.Fatal("found none of the edge's own modules")
	}
}
