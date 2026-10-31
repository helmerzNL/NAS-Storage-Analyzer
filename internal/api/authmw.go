package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/backup"
	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/updates"
)

// Options turn on authentication. Without Auth the server serves everything
// without sign-in; only tests and the benchmark do that.
type Options struct {
	Auth *auth.Service
	// RP is the WebAuthn relying party. Leave RP.ID empty to derive it from the
	// address the browser used (fine for a first try, but passkeys are bound to
	// it: set it explicitly in production).
	RP auth.RelyingParty
	// TrustedProxies are the reverse proxies whose X-Forwarded-* headers are
	// believed. From any other address they are ignored.
	TrustedProxies []*net.IPNet
	// SetupToken must be presented by the setup wizard to create the first
	// administrator, so that whoever reaches a fresh install first cannot take it.
	SetupToken string
	// MetricsToken is the bearer token for /metrics. Without one, /metrics is
	// served to loopback clients only.
	MetricsToken string
	// RequireHTTPS refuses sign-in over plain http (unless via a trusted proxy
	// that says https).
	RequireHTTPS bool
	// Shares and Queue enable share management and scan control (both need a
	// read-write database pool, unlike the query side).
	Shares *shares.Service
	Queue  *jobs.Queue
	// SharesRoot is shown to the UI as the place bind shares must lie in.
	SharesRoot string
	// Static serves the web UI for every non-API path (nil: no UI).
	Static http.Handler
	// PublicURL is the address people reach the app on (https://nas.example.com), for the links in invitations. Empty: the first passkey origin,
	// then the address the administrator is using.
	PublicURL string
	// Updates tells the administrator that a newer release exists (nil: the routes are not registered).
	Updates *updates.Checker
	// Backup makes and restores the backup file from the settings screen (nil: the routes are not registered).
	Backup *backup.Service
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or addresses.
func ParseTrustedProxies(list string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if ip := net.ParseIP(p); ip != nil && ip.To4() != nil {
				p += "/32"
			} else {
				p += "/128"
			}
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return nil, errors.New("invalid trusted proxy " + p)
		}
		out = append(out, n)
	}
	return out, nil
}

type level int

const (
	lvPublic     level = iota // no sign-in
	lvViewer                  // signed in
	lvAdmin                   // signed in as administrator
	lvPending                 // password accepted, second factor outstanding
	lvEnroll                  // password accepted or signed in: may add a second factor
	lvAnySession              // any session (sign-out)
)

type ctxSession struct{}

func sessionOf(r *http.Request) *auth.Session {
	s, _ := r.Context().Value(ctxSession{}).(*auth.Session)
	return s
}

func (s *Server) isTrusted(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range s.opts.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the address of the browser: the connection's, or, behind a
// trusted proxy, the right-most X-Forwarded-For entry that is not itself a
// trusted proxy (anything further left can be forged by the client).
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.isTrusted(r) {
		return host
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		ip := net.ParseIP(p)
		if ip == nil {
			continue
		}
		trusted := false
		for _, n := range s.opts.TrustedProxies {
			if n.Contains(ip) {
				trusted = true
			}
		}
		if !trusted {
			return p
		}
	}
	return host
}

func (s *Server) scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if s.isTrusted(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return "https"
	}
	return "http"
}

func (s *Server) host(r *http.Request) string {
	if s.isTrusted(r) {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			return strings.TrimSpace(strings.Split(h, ",")[0])
		}
	}
	return r.Host
}

const cookieBase = "nasa_session"

func (s *Server) cookieName(r *http.Request) string {
	if s.scheme(r) == "https" {
		return "__Host-" + cookieBase
	}
	return cookieBase
}

func (s *Server) sessionToken(r *http.Request) string {
	for _, n := range []string{"__Host-" + cookieBase, cookieBase} {
		if c, err := r.Cookie(n); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(r), Value: token, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: s.scheme(r) == "https", SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	for _, n := range []string{"__Host-" + cookieBase, cookieBase} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
			Secure: strings.HasPrefix(n, "__Host-"), SameSite: http.SameSiteLaxMode})
	}
}

// originOK implements the CSRF origin check for unsafe requests: a browser
// always sends Origin on cross-site POSTs, and it must name this site.
func (s *Server) originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Not a browser form post. Browsers that omit Origin still send
		// Sec-Fetch-Site, which tells us if the request came from another site.
		if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
			return false
		}
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, s.host(r)) && u.Scheme == s.scheme(r) {
		return true
	}
	for _, o := range s.opts.RP.Origins {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// guard authorises a request for the given level, then runs fn with the usual
// deadline and error handling.
func (s *Server) guard(lv level, fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return s.guardWith(lv, s.handle(fn))
}

// guardStream is guard for long-lived responses (server-sent events): no deadline.
func (s *Server) guardStream(lv level, fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return s.guardWith(lv, func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.fail(w, r, err)
		}
	})
}

func (s *Server) guardWith(lv level, inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			inner(w, r)
			return
		}
		var sess *auth.Session
		if tok := s.sessionToken(r); tok != "" {
			var err error
			if sess, err = s.auth.LookupSession(r.Context(), tok); err != nil && !errors.Is(err, auth.ErrNotFound) {
				s.fail(w, r, err)
				return
			}
		}
		if !isSafeMethod(r.Method) && !s.originOK(r) {
			s.problemCode(w, r, http.StatusForbidden, "request refused", "The request came from another site.", "csrf")
			return
		}
		if status, code, detail := authorize(lv, sess); code != "" {
			s.problemCode(w, r, status, detail, "", code)
			return
		}
		if sess != nil && !isSafeMethod(r.Method) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRFToken)) != 1 {
				s.problemCode(w, r, http.StatusForbidden, "request refused", "Missing or wrong CSRF token.", "csrf")
				return
			}
		}
		inner(w, r.WithContext(context.WithValue(r.Context(), ctxSession{}, sess)))
	}
}

// authorize decides whether a session may use an endpoint of the given level.
func authorize(lv level, sess *auth.Session) (status int, code, title string) {
	if lv == lvPublic {
		return 0, "", ""
	}
	if sess == nil {
		return http.StatusUnauthorized, "unauthenticated", "sign in required"
	}
	switch lv {
	case lvAnySession:
	case lvPending:
		if sess.Stage != auth.StagePending {
			return http.StatusUnauthorized, "unauthenticated", "sign in required"
		}
	case lvEnroll:
		if sess.Stage == auth.StagePending {
			return http.StatusUnauthorized, "mfa_required", "second factor required"
		}
	default:
		switch sess.Stage {
		case auth.StagePending:
			return http.StatusUnauthorized, "mfa_required", "second factor required"
		case auth.StageEnroll:
			return http.StatusForbidden, "enroll_required", "set up a second factor first"
		}
		if lv == lvAdmin && sess.User.Role != "admin" {
			return http.StatusForbidden, "forbidden", "administrator rights required"
		}
	}
	return 0, "", ""
}

func (s *Server) problemCode(w http.ResponseWriter, r *http.Request, status int, title, detail, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(problemBody{Type: "about:blank", Title: title, Status: status, Detail: detail, Code: code, RequestID: requestID(r)})
}

// readJSON decodes a small JSON body into v.
func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return bad("cannot read the request body")
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return bad("the request body is not valid JSON")
	}
	return nil
}

// metricsAllowed authorises /metrics: by bearer token when one is configured,
// otherwise only for loopback clients.
func (s *Server) metricsAllowed(r *http.Request) bool {
	if s.opts.MetricsToken != "" {
		h := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		return h != r.Header.Get("Authorization") && subtle.ConstantTimeCompare([]byte(h), []byte(s.opts.MetricsToken)) == 1
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && !s.isTrusted(r)
}

func (s *Server) rp(r *http.Request) auth.RelyingParty {
	rp := s.opts.RP
	if rp.Name == "" {
		rp.Name = "NAS Storage Analyzer"
	}
	if rp.ID == "" {
		host := s.host(r)
		if h, _, err := net.SplitHostPort(host); err == nil {
			rp.ID = h
		} else {
			rp.ID = host
		}
		rp.Origins = []string{s.scheme(r) + "://" + host}
	}
	return rp
}
