package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/pquerna/otp/totp"

	apispec "github.com/helmerznl/nas-storage-analyzer/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/mounts"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
	"github.com/helmerznl/nas-storage-analyzer/internal/worker"
)

const pw = "correct horse battery"

// startWorker runs a real worker against the environment's database.
func (e *authEnv) startWorker() {
	box, _ := secrets.New(make([]byte, secrets.KeySize))
	m := mounts.New(e.t.TempDir())
	w := worker.New(e.st, shares.NewService(e.st.Pool, box, shares.Options{}, time.UTC), m,
		worker.Config{ID: "api-test", PollEvery: 20 * time.Millisecond, Beat: 50 * time.Millisecond}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(wctx); close(done) }()
	e.t.Cleanup(func() { cancel(); <-done })
}

var (
	specOnce   sync.Once
	specRouter routers.Router
)

// validateExchange checks one request/response pair of a test against the
// OpenAPI document: every HTTP test thereby doubles as a contract test.
func validateExchange(t *testing.T, method, rawURL string, reqBody []byte, reqHdr http.Header, status int, respHdr http.Header, respBody []byte) {
	t.Helper()
	specOnce.Do(func() {
		doc, err := openapi3.NewLoader().LoadFromData(apispec.Spec)
		if err != nil {
			t.Fatalf("load spec: %v", err)
		}
		if specRouter, err = legacy.NewRouter(doc); err != nil {
			t.Fatal(err)
		}
	})
	u, _ := url.Parse(rawURL)
	if !strings.HasPrefix(u.Path, "/api/v1/") || u.Path == "/api/v1/events" {
		return
	}
	req, _ := http.NewRequest(method, u.RequestURI(), bytes.NewReader(reqBody)) // path only: the spec has a relative server URL
	req.Header = reqHdr.Clone()
	route, params, err := specRouter.FindRoute(req)
	if err != nil {
		t.Errorf("%s %s is not in the OpenAPI document: %v", method, u.Path, err)
		return
	}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if status < 300 { // error cases are tested with deliberately wrong requests
		if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil {
			t.Errorf("%s %s: request violates the spec: %v", method, u.Path, err)
		}
	}
	if err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: status, Header: respHdr, Body: io.NopCloser(bytes.NewReader(respBody)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}); err != nil {
		t.Errorf("%s %s -> %d: response violates the spec: %v\n%.500s", method, u.Path, status, err, respBody)
	}
}

type authEnv struct {
	t    *testing.T
	st   *store.Store
	svc  *auth.Service
	srv  *httptest.Server
	opts api.Options
	// routes lists the patterns the server registered.
	routes func() []string
}

func newAuthEnv(t *testing.T, mut func(*api.Options)) *authEnv {
	t.Helper()
	auth.SetFastHashingForTests()
	st := ts.NewStore(t)
	box, _ := secrets.New(make([]byte, secrets.KeySize))
	cfg := auth.DefaultConfig()
	cfg.UserFailLimit = 4
	svc := auth.NewService(st.Pool, box, cfg)
	ro, err := store.OpenReadOnly(context.Background(), st.Pool.Config().ConnString(), 5, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ro.Close)
	opts := api.Options{Auth: svc, SetupToken: "setup-token-123", Queue: jobs.NewQueue(st.Pool),
		Shares: shares.NewService(st.Pool, box, shares.Options{}, time.UTC)}
	if mut != nil {
		mut(&opts)
	}
	h := api.New(ro, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Config{}, opts)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &authEnv{t: t, st: st, svc: svc, srv: srv, opts: opts, routes: h.Routes}
}

// client is a browser-like HTTP client: it keeps cookies and the CSRF token.
type client struct {
	t    *testing.T
	base string
	hc   *http.Client
	csrf string
}

func (e *authEnv) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.srv.URL, hc: &http.Client{Jar: jar}}
}

type reply struct {
	Status int
	Body   map[string]any
	Raw    string
	Header http.Header
}

func (c *client) do(method, path string, body any, hdr ...string) reply {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	var reqBytes []byte
	if body != nil {
		reqBytes, _ = json.Marshal(body)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	validateExchange(c.t, method, c.base+path, reqBytes, req.Header, resp.StatusCode, resp.Header, raw)
	r := reply{Status: resp.StatusCode, Raw: string(raw), Header: resp.Header}
	json.Unmarshal(raw, &r.Body)
	if tok, ok := r.Body["csrf_token"].(string); ok && tok != "" {
		c.csrf = tok
	}
	return r
}

func (c *client) cookie(name string) *http.Cookie {
	u, _ := url.Parse(c.base)
	for _, ck := range c.hc.Jar.Cookies(u) {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func (r reply) code() string { s, _ := r.Body["code"].(string); return s }

func expect(t *testing.T, r reply, status int, code string) {
	t.Helper()
	if r.Status != status || (code != "" && r.code() != code) {
		t.Fatalf("got %d code=%q, want %d code=%q: %s", r.Status, r.code(), status, code, r.Raw)
	}
}

// setupAdmin runs the wizard and TOTP enrolment, returning a signed-in client,
// the TOTP secret and the recovery codes.
func (e *authEnv) setupAdmin() (c *client, secret string, recovery []string) {
	t := e.t
	c = e.client()
	expect(t, c.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "admin", "password": pw}), 200, "")
	r := c.do("POST", "/api/v1/auth/totp/begin", nil)
	expect(t, r, 200, "")
	secret = r.Body["secret"].(string)
	code, _ := totp.GenerateCode(secret, time.Now())
	r = c.do("POST", "/api/v1/auth/totp/confirm", map[string]string{"code": code})
	expect(t, r, 200, "")
	for _, x := range r.Body["recovery_codes"].([]any) {
		recovery = append(recovery, x.(string))
	}
	return c, secret, recovery
}

func TestUnauthenticatedRequestsAreRefused(t *testing.T) {
	e := newAuthEnv(t, nil)
	c := e.client()
	for _, p := range []string{"/api/v1/shares", "/api/v1/auth/me", "/api/v1/users", "/api/v1/audit", "/api/v1/search?q=abc"} {
		expect(t, c.do("GET", p, nil), 401, "unauthenticated")
	}
	expect(t, c.do("GET", "/healthz", nil), 200, "")
	expect(t, c.do("GET", "/openapi.yaml", nil), 200, "")
	st := c.do("GET", "/api/v1/auth/status", nil)
	if st.Body["setup_required"] != true || st.Body["authenticated"] != false {
		t.Errorf("status = %s", st.Raw)
	}
	// Garbage cookies are just "not signed in".
	expect(t, c.do("GET", "/api/v1/shares", nil, "Cookie", "nasa_session=forged-token"), 401, "unauthenticated")
}

func TestSetupWizard(t *testing.T) {
	e := newAuthEnv(t, nil)
	c := e.client()
	expect(t, c.do("POST", "/api/v1/auth/setup", map[string]string{"token": "wrong", "username": "admin", "password": pw}), 403, "invalid_setup_token")
	expect(t, c.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "admin", "password": "short"}), 400, "invalid_request")
	expect(t, c.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "a b", "password": pw}), 400, "invalid_request")
	r := c.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "admin", "password": pw})
	expect(t, r, 200, "")
	if r.Body["status"] != "enroll_required" {
		t.Errorf("status = %v", r.Body["status"])
	}
	// The setup session may only enrol a second factor; nothing else works yet.
	expect(t, c.do("GET", "/api/v1/shares", nil), 403, "enroll_required")
	expect(t, c.do("GET", "/api/v1/auth/me", nil), 403, "enroll_required")
	// The wizard is gone once an account exists, whatever the token.
	expect(t, e.client().do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "x2", "password": pw}), 409, "setup_done")
	// No default credentials exist.
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin"}), 401, "invalid_credentials")
}

func TestSetupTokenBruteForceIsLockedOut(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) {})
	c := e.client()
	var last reply
	for i := 0; i < 40; i++ {
		last = c.do("POST", "/api/v1/auth/setup", map[string]string{"token": fmt.Sprintf("guess-%d", i), "username": "admin", "password": pw})
		if last.Status == 429 {
			break
		}
	}
	expect(t, last, 429, "locked")
	if last.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After on lockout")
	}
	// Even the right token is refused while locked.
	expect(t, c.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "admin", "password": pw}), 429, "locked")
}

func TestEnrolmentLoginAndRecovery(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, secret, recovery := e.setupAdmin()
	if len(recovery) != 10 {
		t.Fatalf("recovery codes = %d", len(recovery))
	}
	// Enrolment finished: the session is active now.
	expect(t, admin.do("GET", "/api/v1/shares", nil), 200, "")
	me := admin.do("GET", "/api/v1/auth/me", nil)
	if u := me.Body["user"].(map[string]any); u["username"] != "admin" || u["role"] != "admin" || u["totp_enabled"] != true {
		t.Errorf("me = %s", me.Raw)
	}
	if strings.Contains(me.Raw, "password") || strings.Contains(me.Raw, "secret") {
		t.Errorf("secrets in /me: %s", me.Raw)
	}

	// Sign out, then in again: password first, then the second factor.
	if r := admin.do("POST", "/api/v1/auth/logout", nil); r.Status != 204 {
		t.Fatalf("logout = %d", r.Status)
	}
	expect(t, admin.do("GET", "/api/v1/shares", nil), 401, "unauthenticated")

	c := e.client()
	r := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "ADMIN", "password": pw})
	expect(t, r, 200, "")
	if r.Body["status"] != "mfa_required" {
		t.Fatalf("login = %s", r.Raw)
	}
	methods := fmt.Sprint(r.Body["methods"])
	if !strings.Contains(methods, "totp") || !strings.Contains(methods, "recovery") {
		t.Errorf("methods = %s", methods)
	}
	// Half signed in: no data, not even the account page.
	expect(t, c.do("GET", "/api/v1/shares", nil), 401, "mfa_required")
	expect(t, c.do("GET", "/api/v1/auth/me", nil), 401, "mfa_required")
	expect(t, c.do("POST", "/api/v1/auth/mfa/totp", map[string]string{"code": "123456"}), 401, "invalid_code")
	// The code used while enrolling is spent; the next step's code works.
	code, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	r = c.do("POST", "/api/v1/auth/mfa/totp", map[string]string{"code": code})
	expect(t, r, 200, "")
	if r.Body["status"] != "ok" {
		t.Fatalf("mfa = %s", r.Raw)
	}
	expect(t, c.do("GET", "/api/v1/shares", nil), 200, "")

	// Recovery code instead of TOTP, once.
	c2 := e.client()
	c2.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw})
	expect(t, c2.do("POST", "/api/v1/auth/mfa/recovery", map[string]string{"code": recovery[0]}), 200, "")
	expect(t, c2.do("GET", "/api/v1/shares", nil), 200, "")
	c3 := e.client()
	c3.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw})
	expect(t, c3.do("POST", "/api/v1/auth/mfa/recovery", map[string]string{"code": recovery[0]}), 401, "invalid_code")
}

func TestSessionCookieAttributes(t *testing.T) {
	e := newAuthEnv(t, nil)
	c, _, _ := e.setupAdmin()
	if c.cookie("nasa_session") == nil {
		t.Fatal("no session cookie in the jar")
	}
	// Look at the raw Set-Cookie header of a login, over plain http.
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"admin","password":%q}`, pw)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sc := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"nasa_session=", "HttpOnly", "SameSite=Lax", "Path=/", "Max-Age=300"} {
		if !strings.Contains(sc, want) {
			t.Errorf("Set-Cookie %q lacks %q", sc, want)
		}
	}
	if strings.Contains(sc, "Secure") {
		t.Errorf("Secure cookie over plain http: %q", sc)
	}
}

func TestCSRFProtection(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	body := map[string]string{"username": "viewer1", "password": pw, "role": "viewer"}
	// Cookie but no token: refused.
	good := admin.csrf
	admin.csrf = ""
	expect(t, admin.do("POST", "/api/v1/users", body), 403, "csrf")
	// Wrong token.
	admin.csrf = "not-the-token"
	expect(t, admin.do("POST", "/api/v1/users", body), 403, "csrf")
	// Right token but a request from another site.
	admin.csrf = good
	expect(t, admin.do("POST", "/api/v1/users", body, "Origin", "https://evil.example"), 403, "csrf")
	expect(t, admin.do("POST", "/api/v1/users", body, "Sec-Fetch-Site", "cross-site"), 403, "csrf")
	// Same origin passes.
	expect(t, admin.do("POST", "/api/v1/users", body, "Origin", e.srv.URL), 201, "")
	// Login is not exempt from the origin check.
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}, "Origin", "https://evil.example"), 403, "csrf")
	// Safe methods need no token.
	admin.csrf = ""
	expect(t, admin.do("GET", "/api/v1/users", nil), 200, "")
}

func TestRolesAndUserAdministration(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	r := admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer1", "display_name": "Vera", "password": pw, "role": "viewer"})
	expect(t, r, 201, "")
	vid := int64(r.Body["id"].(float64))
	expect(t, admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer1", "password": pw, "role": "viewer"}), 409, "conflict")
	expect(t, admin.do("POST", "/api/v1/users", map[string]string{"username": "x", "password": pw, "role": "viewer"}), 400, "invalid_request")
	expect(t, admin.do("POST", "/api/v1/users", map[string]string{"username": "bad-role", "password": pw, "role": "root"}), 400, "")

	// The new user must enrol a second factor at the first sign-in.
	v := e.client()
	r = v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw})
	if r.Body["status"] != "enroll_required" {
		t.Fatalf("first login = %s", r.Raw)
	}
	expect(t, v.do("GET", "/api/v1/shares", nil), 403, "enroll_required")
	sec := v.do("POST", "/api/v1/auth/totp/begin", nil).Body["secret"].(string)
	code, _ := totp.GenerateCode(sec, time.Now())
	expect(t, v.do("POST", "/api/v1/auth/totp/confirm", map[string]string{"code": code}), 200, "")
	// A viewer reads but administers nothing.
	expect(t, v.do("GET", "/api/v1/shares", nil), 200, "")
	for _, p := range []string{"/api/v1/users", "/api/v1/audit"} {
		expect(t, v.do("GET", p, nil), 403, "forbidden")
	}
	expect(t, v.do("POST", "/api/v1/users", map[string]string{"username": "evil1", "password": pw, "role": "admin"}), 403, "forbidden")
	expect(t, v.do("DELETE", fmt.Sprintf("/api/v1/users/%d", vid), nil), 403, "forbidden")

	// Admin: promote, disable (kills sessions), reset MFA, delete.
	expect(t, admin.do("PATCH", fmt.Sprintf("/api/v1/users/%d", vid), map[string]any{"disabled": true}), 200, "")
	expect(t, v.do("GET", "/api/v1/shares", nil), 401, "unauthenticated")
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw}), 401, "invalid_credentials")
	expect(t, admin.do("PATCH", fmt.Sprintf("/api/v1/users/%d", vid), map[string]any{"disabled": false}), 200, "")
	expect(t, admin.do("POST", fmt.Sprintf("/api/v1/users/%d/reset-mfa", vid), nil), 204, "")
	again := e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw})
	if again.Body["status"] != "enroll_required" {
		t.Errorf("after MFA reset: %s", again.Raw)
	}
	expect(t, admin.do("POST", fmt.Sprintf("/api/v1/users/%d/password", vid), map[string]string{"password": "another-long-password"}), 204, "")
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": "another-long-password"}), 200, "")
	expect(t, admin.do("DELETE", fmt.Sprintf("/api/v1/users/%d", vid), nil), 204, "")

	// The last administrator is protected.
	me := admin.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any)
	aid := int64(me["id"].(float64))
	expect(t, admin.do("DELETE", fmt.Sprintf("/api/v1/users/%d", aid), nil), 409, "last_admin")
	expect(t, admin.do("PATCH", fmt.Sprintf("/api/v1/users/%d", aid), map[string]any{"role": "viewer"}), 409, "last_admin")
	expect(t, admin.do("DELETE", "/api/v1/users/abc", nil), 400, "")
	expect(t, admin.do("DELETE", "/api/v1/users/99999", nil), 404, "")
}

func TestLockoutOverHTTP(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.setupAdmin()
	c := e.client()
	var r reply
	for i := 0; i < 6; i++ {
		r = c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": fmt.Sprintf("wrong-password-%d", i)})
	}
	expect(t, r, 429, "locked")
	if r.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	// Locked even for the right password, and unknown users look the same.
	expect(t, c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}), 429, "locked")
}

func TestSecondFactorFailuresCountTowardLockout(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.setupAdmin()
	c := e.client()
	c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw})
	var r reply
	for i := 0; i < 8; i++ {
		r = c.do("POST", "/api/v1/auth/mfa/totp", map[string]string{"code": fmt.Sprintf("00000%d", i)})
		if r.Status == 429 {
			break
		}
	}
	expect(t, r, 429, "locked")
}

func TestPasswordAndFactorManagement(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, recovery := e.setupAdmin()
	// Sensitive actions need the current password again.
	expect(t, admin.do("POST", "/api/v1/auth/recovery-codes", map[string]string{"password": "wrong-password!"}), 401, "invalid_credentials")
	r := admin.do("POST", "/api/v1/auth/recovery-codes", map[string]string{"password": pw})
	expect(t, r, 200, "")
	if n := len(r.Body["recovery_codes"].([]any)); n != 10 {
		t.Errorf("codes = %d", n)
	}
	// The old set is dead.
	c := e.client()
	c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw})
	expect(t, c.do("POST", "/api/v1/auth/mfa/recovery", map[string]string{"code": recovery[3]}), 401, "invalid_code")

	// TOTP is the only factor: it cannot be removed.
	expect(t, admin.do("DELETE", "/api/v1/auth/totp", map[string]string{"password": pw}), 409, "last_factor")
	// Password change.
	expect(t, admin.do("POST", "/api/v1/auth/password", map[string]string{"current": "wrong-password!", "new": "a-new-long-password"}), 401, "invalid_credentials")
	expect(t, admin.do("POST", "/api/v1/auth/password", map[string]string{"current": pw, "new": "short"}), 400, "invalid_request")
	if r := admin.do("POST", "/api/v1/auth/password", map[string]string{"current": pw, "new": "a-new-long-password"}); r.Status != 204 {
		t.Fatalf("change = %d %s", r.Status, r.Raw)
	}
	expect(t, admin.do("GET", "/api/v1/shares", nil), 200, "") // own session survives
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "a-new-long-password"}), 200, "")
}

func TestPasskeysOverHTTP(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) {
		o.RP = auth.RelyingParty{ID: "nas.example.test", Name: "NAS", Origins: []string{"https://nas.example.test"}}
	})
	const origin = "https://nas.example.test"
	c := e.client()
	expect(t, c.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "admin", "password": pw}), 200, "")

	// A passkey alone satisfies the second-factor requirement during enrolment.
	va := ts.NewVirtualAuthenticator()
	r := c.do("POST", "/api/v1/auth/passkeys/register/begin", nil)
	expect(t, r, 200, "")
	ceremony := r.Body["ceremony"].(string)
	creation := rebuildCreation(t, r.Body["options"])
	reg := va.Create(creation, origin)
	body, _ := io.ReadAll(reg.Body)
	r = c.do("POST", "/api/v1/auth/passkeys/register/finish?ceremony="+ceremony+"&name=Laptop", json.RawMessage(body))
	expect(t, r, 200, "")
	if r.Body["status"] != "ok" || len(r.Body["recovery_codes"].([]any)) != 10 {
		t.Fatalf("finish = %s", r.Raw)
	}
	expect(t, c.do("GET", "/api/v1/shares", nil), 200, "")
	list := c.do("GET", "/api/v1/auth/passkeys", nil)
	if items := list.Body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "Laptop" {
		t.Errorf("passkeys = %s", list.Raw)
	}
	expect(t, c.do("DELETE", "/api/v1/auth/passkeys/"+list.Body["items"].([]any)[0].(map[string]any)["id"].(string), nil), 409, "last_factor")

	// Passwordless sign-in from a fresh browser.
	b := e.client()
	r = b.do("POST", "/api/v1/auth/passkey/login/begin", nil)
	expect(t, r, 200, "")
	assertion := rebuildAssertion(t, r.Body["options"])
	ans := va.Get(assertion, origin)
	ansBody, _ := io.ReadAll(ans.Body)
	r = b.do("POST", "/api/v1/auth/passkey/login/finish?ceremony="+r.Body["ceremony"].(string), json.RawMessage(ansBody))
	expect(t, r, 200, "")
	if r.Body["status"] != "ok" {
		t.Fatalf("passwordless = %s", r.Raw)
	}
	expect(t, b.do("GET", "/api/v1/shares", nil), 200, "")

	// Password + passkey as second factor.
	p := e.client()
	r = p.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw})
	if r.Body["status"] != "mfa_required" || !strings.Contains(fmt.Sprint(r.Body["methods"]), "passkey") {
		t.Fatalf("login = %s", r.Raw)
	}
	r = p.do("POST", "/api/v1/auth/mfa/passkey/begin", nil)
	expect(t, r, 200, "")
	assertion = rebuildAssertion(t, r.Body["options"])
	ans = va.Get(assertion, origin)
	ansBody, _ = io.ReadAll(ans.Body)
	expect(t, p.do("POST", "/api/v1/auth/mfa/passkey/finish?ceremony="+r.Body["ceremony"].(string), json.RawMessage(ansBody)), 200, "")
	expect(t, p.do("GET", "/api/v1/shares", nil), 200, "")

	// A bad assertion is refused and counts as a failure.
	q := e.client()
	r = q.do("POST", "/api/v1/auth/passkey/login/begin", nil)
	other := ts.NewVirtualAuthenticator()
	other.UserHandle = va.UserHandle
	bad := other.Get(rebuildAssertion(t, r.Body["options"]), origin)
	badBody, _ := io.ReadAll(bad.Body)
	expect(t, q.do("POST", "/api/v1/auth/passkey/login/finish?ceremony="+r.Body["ceremony"].(string), json.RawMessage(badBody)), 401, "invalid_code")
}

func TestTrustedProxyHeaders(t *testing.T) {
	proxy := mustCIDR(t, "10.0.0.0/8")
	e := newAuthEnv(t, func(o *api.Options) { o.TrustedProxies = proxy })
	e.setupAdmin()
	handler := func() http.Handler {
		ro, _ := store.OpenReadOnly(context.Background(), e.st.Pool.Config().ConnString(), 3, time.Second)
		t.Cleanup(ro.Close)
		return api.New(ro, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Config{}, e.opts)
	}()
	login := func(remote string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://nas.example.test/api/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"admin","password":%q}`, pw)))
		req.RemoteAddr = remote
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	// From a trusted proxy that says https: Secure, __Host- cookie, and the
	// address in the audit log is the client's, not the proxy's.
	rec := login("10.1.2.3:5555", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "203.0.113.9, 10.9.9.9", "Origin": "https://nas.example.test", "X-Forwarded-Host": "nas.example.test"})
	if rec.Code != 200 {
		t.Fatalf("login via proxy = %d %s", rec.Code, rec.Body)
	}
	sc := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(sc, "__Host-nasa_session=") || !strings.Contains(sc, "Secure") {
		t.Errorf("cookie via https proxy = %q", sc)
	}
	var ip string
	e.st.Pool.QueryRow(context.Background(), `SELECT ip FROM audit_log WHERE action = 'login.password_ok' ORDER BY id DESC LIMIT 1`).Scan(&ip)
	if ip != "203.0.113.9" {
		t.Errorf("audited address = %q, want the client behind the proxy", ip)
	}
	// From an untrusted peer the same headers are ignored: no forged address,
	// no pretending to be https.
	rec = login("192.0.2.77:4444", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "203.0.113.200"})
	sc = rec.Header().Get("Set-Cookie")
	if strings.Contains(sc, "Secure") || strings.HasPrefix(sc, "__Host-") {
		t.Errorf("forged X-Forwarded-Proto honoured: %q", sc)
	}
	e.st.Pool.QueryRow(context.Background(), `SELECT ip FROM audit_log WHERE action = 'login.password_ok' ORDER BY id DESC LIMIT 1`).Scan(&ip)
	if ip != "192.0.2.77" {
		t.Errorf("audited address = %q, forged X-Forwarded-For honoured", ip)
	}
}

func TestRequireHTTPS(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) { o.RequireHTTPS = true })
	e.setupAdmin() // setup itself is allowed; only sign-in is refused over http
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}), 400, "https_required")
}

func TestMetricsAccess(t *testing.T) {
	e := newAuthEnv(t, nil)
	// No token configured: loopback only. The test server is on loopback.
	if r := e.client().do("GET", "/metrics", nil); r.Status != 200 {
		t.Errorf("loopback metrics = %d", r.Status)
	}
	e2 := newAuthEnv(t, func(o *api.Options) { o.MetricsToken = "scrape-me" })
	expect(t, e2.client().do("GET", "/metrics", nil), 401, "unauthenticated")
	expect(t, e2.client().do("GET", "/metrics", nil, "Authorization", "Bearer wrong"), 401, "unauthenticated")
	if r := e2.client().do("GET", "/metrics", nil, "Authorization", "Bearer scrape-me"); r.Status != 200 {
		t.Errorf("metrics with token = %d", r.Status)
	}
}

func TestAuditTrail(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong-password!"})
	admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer1", "password": pw, "role": "viewer"})
	r := admin.do("GET", "/api/v1/audit?limit=50", nil)
	expect(t, r, 200, "")
	seen := map[string]bool{}
	for _, it := range r.Body["items"].([]any) {
		seen[it.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"setup.complete", "login.failure", "user.create", "mfa.totp_enabled", "login.success"} {
		if !seen[want] {
			t.Errorf("audit lacks %q; has %v", want, seen)
		}
	}
	if strings.Contains(r.Raw, pw) || strings.Contains(r.Raw, "wrong-password") {
		t.Error("a password ended up in the audit log")
	}
	if r := admin.do("GET", "/api/v1/audit?action=login.", nil); len(r.Body["items"].([]any)) < 2 {
		t.Errorf("prefix filter: %s", r.Raw)
	}
}

func mustCIDR(t *testing.T, s string) []*net.IPNet {
	t.Helper()
	n, err := api.ParseTrustedProxies(s)
	if err != nil || len(n) == 0 {
		t.Fatal(err)
	}
	return n
}

// The API sends WebAuthn options as JSON; the virtual authenticator works on
// the library's types, so convert back as a browser's JSON parsing would.
func rebuildCreation(t *testing.T, options any) *protocol.CredentialCreation {
	t.Helper()
	b, _ := json.Marshal(options)
	var c protocol.CredentialCreation
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func rebuildAssertion(t *testing.T, options any) *protocol.CredentialAssertion {
	t.Helper()
	b, _ := json.Marshal(options)
	var a protocol.CredentialAssertion
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	return &a
}

// codeFor returns the TOTP code for a base32 secret now.
func codeFor(secret string) string {
	c, _ := totp.GenerateCode(secret, time.Now())
	return c
}

func TestSpecAndRoutesMatchExactly(t *testing.T) {
	e := newAuthEnv(t, nil)
	doc, err := openapi3.NewLoader().LoadFromData(apispec.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("the OpenAPI document is invalid: %v", err)
	}
	inSpec := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			inSpec[method+" "+path] = true
		}
	}
	inCode := map[string]bool{}
	for _, p := range e.routes() {
		inCode[p] = true
	}
	for p := range inSpec {
		if !inCode[p] {
			t.Errorf("documented but not routed: %s", p)
		}
	}
	for p := range inCode {
		if !inSpec[p] {
			t.Errorf("routed but not documented: %s", p)
		}
	}
	if len(inSpec) < 50 {
		t.Errorf("only %d operations in the spec", len(inSpec))
	}
}
