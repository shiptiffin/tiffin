package manifest

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func routesOf(m *Manifest) map[string][]string {
	out := map[string][]string{}
	for name, a := range m.Apps {
		out[name] = a.Routes
	}
	return out
}

func TestMainApp(t *testing.T) {
	web, worker := App{}, App{Role: RoleWorker}
	for _, c := range []struct {
		project string
		apps    map[string]App
		order   []string
		want    string
	}{
		{"shop", map[string]App{}, nil, ""},
		{"shop", map[string]App{"jobs": worker}, nil, ""},
		{"shop", map[string]App{"site": web, "jobs": worker}, nil, "site"},                                    // the only web app
		{"shop", map[string]App{"api": web, "shop": web, "web": web}, []string{"api", "web", "shop"}, "shop"}, // named like the project
		{"shop", map[string]App{"api": web, "web": web}, []string{"api", "web"}, "web"},                       // named web
		{"shop", map[string]App{"docs": web, "site": web}, []string{"site", "docs"}, "site"},                  // first declared
		{"shop", map[string]App{"docs": web, "site": web}, nil, "docs"},                                       // order unknown: first by name
		{"shop", map[string]App{"jobs": worker, "site": web, "api": web}, []string{"jobs", "site", "api"}, "site"},
		{"shop", map[string]App{"web": worker, "site": web, "api": web}, nil, "api"},
		{"shop", map[string]App{"docs": web, "site": web, "blog": web}, []string{"site"}, "site"}, // apps the order leaves out come after, by name
	} {
		if got := MainApp(c.project, c.apps, c.order); got != c.want {
			t.Errorf("MainApp(%s, %v, %v) = %q, want %q", c.project, slices.Sorted(maps.Keys(c.apps)), c.order, got, c.want)
		}
	}
}

func TestDefaultAddresses(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		want      map[string][]string
	}{
		{"one app is the project", `{"project":"shop","apps":{"api":{}}}`,
			map[string][]string{"api": {"shop"}}},
		{"web is main, others project-app", `{"project":"shop","apps":{"web":{},"docs":{},"jobs":{"role":"worker"}}}`,
			map[string][]string{"web": {"shop"}, "docs": {"shop-docs"}, "jobs": nil}},
		{"app named like the project", `{"project":"shop","apps":{"shop":{},"web":{}}}`,
			map[string][]string{"shop": {"shop"}, "web": {"shop-web"}}},
		{"first declared", `{"project":"shop","apps":{"site":{},"docs":{}}}`,
			map[string][]string{"site": {"shop"}, "docs": {"shop-docs"}}},
		{"first declared, the other way", `{"project":"shop","apps":{"docs":{},"site":{}}}`,
			map[string][]string{"docs": {"shop"}, "site": {"shop-site"}}},
		{"first declared web app", `{"project":"shop","apps":{"jobs":{"role":"worker"},"site":{},"api":{}},"services":{"database":{}}}`,
			map[string][]string{"jobs": nil, "site": {"shop"}, "api": {"shop-api"}}},
		{"explicit routes are kept", `{"project":"shop","apps":{"web":{"routes":["Example.com"]},"docs":{}}}`,
			map[string][]string{"web": {"example.com"}, "docs": {"shop-docs"}}},
		{"a taken default falls back to project-app", `{"project":"shop","apps":{"web":{},"admin":{"routes":["shop"]}}}`,
			map[string][]string{"web": {"shop-web"}, "admin": {"shop"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := Parse([]byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got := routesOf(m); !reflect.DeepEqual(got, c.want) {
				t.Errorf("routes = %v, want %v", got, c.want)
			}
			// The rendered config leaves defaults out and reads back the same.
			again, err := Parse(mustJSON(t, m))
			if err != nil || !reflect.DeepEqual(routesOf(again), c.want) {
				t.Errorf("canonical round trip: %v %v", err, again)
			}
		})
	}
}

func mustJSON(t *testing.T, m *Manifest) []byte {
	t.Helper()
	b, err := Canonical(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRenderLeavesDefaultAddressesOut(t *testing.T) {
	m, err := Parse([]byte(`{"project":"shop","apps":{"web":{},"docs":{},"site":{"routes":["site"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(RenderConfig(m, ""))
	if strings.Count(cfg, "routes") != 1 || !strings.Contains(cfg, `routes: ["site"]`) {
		t.Fatalf("only the chosen route is written out:\n%s", cfg)
	}
}

func TestAddressesOnBox(t *testing.T) {
	parse := func(raw string, have map[string][]string) (map[string][]string, []string) {
		t.Helper()
		m, older, err := ParseOnBox([]byte(raw), func(project string) (map[string][]string, error) {
			if project != "shop" {
				t.Fatalf("asked about %q", project)
			}
			return have, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return routesOf(m), older
	}
	// An app deployed before addresses were named after the project keeps its name.
	got, older := parse(`{"project":"shop","apps":{"api":{},"web":{}}}`, map[string][]string{"api": {"api"}, "web": {"web"}})
	if want := map[string][]string{"api": {"api"}, "web": {"web"}}; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(older, []string{"api", "web"}) {
		t.Errorf("old addresses: %v %v", got, older)
	}
	// A new app beside them gets the new default, and project is free for it.
	got, _ = parse(`{"project":"shop","apps":{"api":{},"web":{},"docs":{}}}`, map[string][]string{"api": {"api"}, "web": {"web"}})
	if want := map[string][]string{"api": {"api"}, "web": {"web"}, "docs": {"shop-docs"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("new app beside old ones: %v", got)
	}
	// Adding an app that becomes the main one does not move the app at <project>.
	got, older = parse(`{"project":"shop","apps":{"site":{},"docs":{}}}`, map[string][]string{"site": {"shop"}})
	if want := map[string][]string{"site": {"shop"}, "docs": {"shop-docs"}}; !reflect.DeepEqual(got, want) || older != nil {
		t.Errorf("main app added: %v %v", got, older)
	}
	// A project whose main app was picked by name keeps it when the config lists another web app first.
	got, older = parse(`{"project":"shop","apps":{"site":{},"docs":{}}}`, map[string][]string{"docs": {"shop"}, "site": {"shop-site"}})
	if want := map[string][]string{"site": {"shop-site"}, "docs": {"shop"}}; !reflect.DeepEqual(got, want) || older != nil {
		t.Errorf("main app picked by name: %v %v", got, older)
	}
	// Routes the config sets win; an app whose chosen route is dropped gets the default.
	got, _ = parse(`{"project":"shop","apps":{"api":{"routes":["shop"]},"web":{}}}`, map[string][]string{"api": {"api"}, "web": {"custom"}})
	if want := map[string][]string{"api": {"shop"}, "web": {"shop-web"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("explicit routes: %v", got)
	}
	// Plain Parse knows nothing of the box.
	m, err := Parse([]byte(`{"project":"shop","apps":{"api":{}}}`))
	if err != nil || !reflect.DeepEqual(m.Apps["api"].Routes, []string{"shop"}) {
		t.Errorf("Parse: %v %v", err, m)
	}
}

func TestLongDefaultAddress(t *testing.T) {
	long := strings.Repeat("p", 40)
	_, err := Parse([]byte(`{"project":"` + long + `","apps":{"web":{},"` + strings.Repeat("a", 30) + `":{}}}`))
	if err == nil || !strings.Contains(err.Error(), "longer than 63 characters") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse([]byte(`{"project":"` + long + `","apps":{"web":{},"docs":{}}}`)); err != nil {
		t.Fatal(err)
	}
}
