package cloud

import (
	"fmt"
	"slices"
	"strings"
)

// A managed box's name is its address, <name>.shiptiffin.app, its Hetzner
// server's name and its hostname. The website checks names with the same
// rules (site/lib/cloud/names.ts); keep the two lists in step.

const (
	NameMin = 3
	NameMax = 30
)

// Reserved names: ours, infrastructure words, and names that would read as
// ShipTiffin or another brand speaking (phishing bait).
var Reserved = []string{
	"abuse", "account", "accounts", "admin", "administrator", "api", "app", "apps", "assets", "auth",
	"billing", "blog", "box", "boxes", "cdn", "cloud", "console", "control", "dashboard", "dev", "dns",
	"docs", "download", "email", "files", "ftp", "git", "help", "hetzner", "home", "imap", "info",
	"internal", "invoice", "legal", "login", "logout", "mail", "manage", "monitor", "mx", "news", "ns",
	"ns1", "ns2", "ns3", "ns4", "oauth", "official", "password", "pay", "payment", "payments", "pop",
	"pop3", "portal", "postmaster", "preview", "privacy", "root", "s3", "secure", "security", "server",
	"shiptiffin", "signin", "signup", "smtp", "sso", "staging", "start", "static", "status", "stripe",
	"support", "system", "team", "terms", "test", "tiffin", "update", "updates", "verify", "wallet",
	"webmail", "www",
	// projects on ShipTiffin's own box, which serves them under the same zone
	"website", "provisioner", "releases", "hello",
	// brands most often phished
	"apple", "amazon", "google", "gmail", "microsoft", "outlook", "office", "paypal", "facebook",
	"instagram", "whatsapp", "netflix", "binance", "coinbase", "metamask", "github", "chase", "wellsfargo",
}

// ValidName checks a managed box's name.
func ValidName(name string) error {
	if len(name) < NameMin || len(name) > NameMax {
		return fmt.Errorf("use %d to %d characters", NameMin, NameMax)
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
		if !ok {
			return fmt.Errorf("use lowercase letters, digits and dashes")
		}
		if i == 0 && !(r >= 'a' && r <= 'z') {
			return fmt.Errorf("start with a letter")
		}
	}
	if strings.HasSuffix(name, "-") {
		return fmt.Errorf("end with a letter or digit")
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("no double dashes") // xn-- (punycode) and Tiffin's own d-…--app names
	}
	if slices.Contains(Reserved, name) {
		return fmt.Errorf("%s is reserved", name)
	}
	for _, b := range ReservedParts {
		if strings.Contains(name, b) {
			return fmt.Errorf("names containing %q are reserved", b)
		}
	}
	return nil
}

// ReservedParts may not appear anywhere in a name.
var ReservedParts = []string{"shiptiffin", "paypal", "google", "microsoft", "amazon", "signin", "login", "verify", "wallet"}
