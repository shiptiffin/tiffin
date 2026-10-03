package edge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"math/bits"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// The proof-of-work challenge (Anubis-style, our own code).
//
// A request on a challenged host without a valid clearance cookie gets a
// small page that finds a nonce such that SHA-256(token + nonce) starts
// with Difficulty zero bits, then posts it to ChallengePath. The edge
// checks the proof and sets a clearance cookie: HMAC-signed, bound to the
// host, the client's network (IPv4 /24, IPv6 /48), a hash of its User-Agent
// and an expiry. Tokens are signed the same way and expire after 10 minutes.
//
// Never challenged: /.well-known/*, /robots.txt, CORS preflights and
// requests carrying a bearer token (API clients). No crawler allow-list:
// one cannot be verified without reverse-DNS lookups we do not do.

const (
	minDifficulty = 8
	maxDifficulty = 24
	clearanceName = "__Host-tiffin-clearance"
	tokenTTL      = 10 * time.Minute
)

func init() { caddy.RegisterModule(ChallengeHandler{}) }

// ChallengeHandler is the Caddy module http.handlers.tiffin_challenge.
type ChallengeHandler struct {
	Secret     string         `json:"secret"`     // hex, at least 32 bytes
	Difficulty int            `json:"difficulty"` // leading zero bits
	TTL        caddy.Duration `json:"ttl,omitempty"`

	key []byte
	now func() time.Time
}

// CaddyModule returns the Caddy module information.
func (ChallengeHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.tiffin_challenge",
		New: func() caddy.Module { return new(ChallengeHandler) },
	}
}

// Provision decodes the secret and applies defaults.
func (h *ChallengeHandler) Provision(caddy.Context) error {
	key, err := hex.DecodeString(h.Secret)
	if err != nil || len(key) < 32 {
		return errors.New("tiffin_challenge: secret must be at least 32 bytes of hex")
	}
	if h.Difficulty < minDifficulty || h.Difficulty > maxDifficulty {
		return fmt.Errorf("tiffin_challenge: difficulty %d: want %d–%d", h.Difficulty, minDifficulty, maxDifficulty)
	}
	if h.TTL <= 0 {
		h.TTL = caddy.Duration(24 * time.Hour)
	}
	h.key = key
	if h.now == nil {
		h.now = time.Now
	}
	return nil
}

// ServeHTTP lets cleared and exempt requests through and challenges the rest.
func (h *ChallengeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if exempt(r) {
		return next.ServeHTTP(w, r)
	}
	bind := binding(r)
	host := strings.ToLower(r.Host)
	if r.URL.Path == ChallengePath {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return nil
		}
		return h.verify(w, r, host, bind)
	}
	if c, err := r.Cookie(clearanceName); err == nil && h.cleared(c.Value, host, bind) {
		return next.ServeHTTP(w, r)
	}
	h.challenge(w, r, host, bind, http.StatusForbidden, "")
	return nil
}

func exempt(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/.well-known/") || p == "/robots.txt" || r.Method == http.MethodOptions {
		return true
	}
	a := r.Header.Get("Authorization")
	return len(a) > 7 && strings.EqualFold(a[:7], "bearer ")
}

// binding identifies a client loosely: its network and its User-Agent.
func binding(r *http.Request) string {
	ip, _ := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey).(string)
	if ip == "" {
		ip, _, _ = strings.Cut(r.RemoteAddr, ":")
	}
	network := ip
	if a, err := netip.ParseAddr(ip); err == nil {
		a = a.Unmap()
		bitsLen := 48
		if a.Is4() {
			bitsLen = 24
		}
		if pfx, err := a.Prefix(bitsLen); err == nil {
			network = pfx.String()
		}
	}
	sum := sha256.Sum256([]byte(network + "\n" + r.UserAgent()))
	return hex.EncodeToString(sum[:8])
}

func (h *ChallengeHandler) sign(kind, host, payload string) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte(kind + "\n" + host + "\n" + payload))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// newToken returns "1.<random>.<unix>.<bits>.<bind>.<sig>" (hex, digits and dots).
func (h *ChallengeHandler) newToken(host, bind string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	payload := fmt.Sprintf("1.%s.%d.%d.%s", hex.EncodeToString(b[:]), h.now().Unix(), h.Difficulty, bind)
	return payload + "." + h.sign("challenge", host, payload)
}

// checkToken returns the difficulty a valid token demands.
func (h *ChallengeHandler) checkToken(tok, host, bind string) (int, error) {
	i := strings.LastIndexByte(tok, '.')
	if i < 0 || len(tok) > 256 {
		return 0, errors.New("malformed token")
	}
	payload, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(h.sign("challenge", host, payload))) {
		return 0, errors.New("bad signature")
	}
	f := strings.Split(payload, ".")
	if len(f) != 5 || f[0] != "1" {
		return 0, errors.New("malformed token")
	}
	issued, err1 := strconv.ParseInt(f[2], 10, 64)
	diff, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil {
		return 0, errors.New("malformed token")
	}
	age := h.now().Sub(time.Unix(issued, 0))
	if age < -time.Minute || age > tokenTTL {
		return 0, errors.New("expired token")
	}
	if f[4] != bind {
		return 0, errors.New("token is for another client")
	}
	return diff, nil
}

// LeadingZeroBits counts the zero bits at the start of SHA-256(token+nonce).
func LeadingZeroBits(token, nonce string) int {
	sum := sha256.Sum256([]byte(token + nonce))
	n := 0
	for i := 0; i < len(sum); i += 8 {
		w := binary.BigEndian.Uint64(sum[i:])
		z := bits.LeadingZeros64(w)
		n += z
		if z < 64 {
			break
		}
	}
	return n
}

// SolveChallenge finds a nonce for token at difficulty bits (for tests and
// scripted clients; browsers run the same search in JavaScript).
func SolveChallenge(token string, difficulty int) string {
	for n := 0; ; n++ {
		s := strconv.Itoa(n)
		if LeadingZeroBits(token, s) >= difficulty {
			return s
		}
	}
}

func (h *ChallengeHandler) verify(w http.ResponseWriter, r *http.Request, host, bind string) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		h.challenge(w, r, host, bind, http.StatusBadRequest, "")
		return nil
	}
	tok, nonce := r.PostForm.Get("token"), r.PostForm.Get("nonce")
	next := safeNext(r.PostForm.Get("next"))
	diff, err := h.checkToken(tok, host, bind)
	if err == nil && (len(nonce) == 0 || len(nonce) > 20 || LeadingZeroBits(tok, nonce) < diff) {
		err = errors.New("proof does not check out")
	}
	if err != nil {
		h.challenge(w, r, host, bind, http.StatusForbidden, next)
		return nil
	}
	exp := h.now().Add(time.Duration(h.TTL)).Unix()
	payload := fmt.Sprintf("1.%d.%s", exp, bind)
	http.SetCookie(w, &http.Cookie{
		Name:     clearanceName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(payload + "." + h.sign("clearance", host, payload))),
		Path:     "/",
		MaxAge:   int(time.Duration(h.TTL).Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, next, http.StatusSeeOther)
	return nil
}

func (h *ChallengeHandler) cleared(value, host, bind string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	s := string(raw)
	i := strings.LastIndexByte(s, '.')
	if i < 0 {
		return false
	}
	payload, sig := s[:i], s[i+1:]
	if !hmac.Equal([]byte(sig), []byte(h.sign("clearance", host, payload))) {
		return false
	}
	f := strings.Split(payload, ".")
	if len(f) != 3 || f[0] != "1" || f[2] != bind {
		return false
	}
	exp, err := strconv.ParseInt(f[1], 10, 64)
	return err == nil && h.now().Unix() < exp
}

// safeNext keeps redirects on this host: a path, never "//..." or "/\...".
func safeNext(next string) string {
	if next == "" || len(next) > 2048 || next[0] != '/' || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	for _, c := range next {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	if strings.HasPrefix(next, ChallengePath) {
		return "/"
	}
	return next
}

func (h *ChallengeHandler) challenge(w http.ResponseWriter, r *http.Request, host, bind string, status int, next string) {
	if next == "" {
		next = safeNext(r.URL.RequestURI())
	}
	hdr := w.Header()
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Tiffin-Challenge", "required")
	hdr.Set("X-Robots-Tag", "noindex")
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		hdr.Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"code":"challenge_required","detail":"This site is checking visitors while it is under heavy traffic. Open it in a browser to pass the check, or call it with an API token (Authorization: Bearer ...)."}` + "\n"))
		return
	}
	var nb [12]byte
	_, _ = rand.Read(nb[:])
	nonce := base64.RawStdEncoding.EncodeToString(nb[:])
	note := ""
	if r.URL.Path == ChallengePath {
		note = `<p>That check didn't go through, so it's running again.</p>`
	}
	page := strings.NewReplacer(
		"{{NONCE}}", nonce,
		"{{TOKEN}}", h.newToken(host, bind),
		"{{BITS}}", strconv.Itoa(h.Difficulty),
		"{{NEXT}}", html.EscapeString(next),
		"{{NOTE}}", note,
	).Replace(challengeHTML)
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Security-Policy", pageCSP(nonce))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(page))
	}
}

var (
	_ caddy.Provisioner           = (*ChallengeHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*ChallengeHandler)(nil)
)
