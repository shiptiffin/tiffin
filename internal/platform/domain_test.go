package platform

import "testing"

func TestHostsWithAndWithoutAppsDomain(t *testing.T) {
	p := &Platform{Domain: "example.com", PublicURL: "https://dashboard.example.com"}
	if p.AppsDomain() != "example.com" || p.Host("shop") != "shop.example.com" || p.DashboardHost() != "dashboard.example.com" {
		t.Errorf("one domain: apps %s, host %s, dashboard %s", p.AppsDomain(), p.Host("shop"), p.DashboardHost())
	}
	if !p.IsBoxHost("Shop.example.com") || p.IsBoxHost("shop.example.app") || p.IsBoxHost("example.com") {
		t.Error("IsBoxHost with one domain")
	}

	p.Reach = Reach{AppsDomain: "example.app", Dashboard: "admin"}
	if p.AppsDomain() != "example.app" || p.Host("shop") != "shop.example.app" {
		t.Errorf("apps domain: apps %s, host %s", p.AppsDomain(), p.Host("shop"))
	}
	if p.DashboardHost() != "admin.example.com" {
		t.Errorf("the dashboard stays on the box domain: %s", p.DashboardHost())
	}
	if !p.IsBoxHost("shop.example.app") || p.IsBoxHost("shop.example.com") || p.IsBoxHost("a.b.example.app") {
		t.Error("IsBoxHost with an apps domain")
	}
	if got := p.URL(p.Host("shop")); got != "https://shop.example.app" {
		t.Errorf("URL = %s", got)
	}
	p.PublicURL = "https://admin.example.com:8443"
	if got := p.URL(p.Host("shop")); got != "https://shop.example.app:8443" {
		t.Errorf("URL with a port = %s", got)
	}
}
