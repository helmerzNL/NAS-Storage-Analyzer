package api_test

import (
	"strings"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
)

// signInAgain signs the administrator in once more, in another browser: password, then a recovery code (an authenticator code
// can be used once per 30 seconds, a recovery code once at all, and there are ten).
func (e *authEnv) signInAgain(recoveryCode, userAgent string) *client {
	e.t.Helper()
	c := e.client()
	expect(e.t, c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}, "User-Agent", userAgent), 200, "")
	r := c.do("POST", "/api/v1/auth/mfa/recovery", map[string]string{"code": recoveryCode}, "User-Agent", userAgent)
	expect(e.t, r, 200, "")
	if r.Body["status"] != "ok" {
		e.t.Fatalf("second sign-in: %s", r.Raw)
	}
	return c
}

func sessionItems(t *testing.T, c *client) []map[string]any {
	t.Helper()
	r := c.do("GET", "/api/v1/auth/sessions", nil)
	expect(t, r, 200, "")
	var out []map[string]any
	for _, it := range r.Body["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func TestSessionsAreListedAndOneCanBeSignedOut(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, recovery := e.setupAdmin()

	mine := sessionItems(t, admin)
	if len(mine) != 1 || mine[0]["current"] != true || mine[0]["ip"] == "" || mine[0]["id"] == "" {
		t.Fatalf("one session, this one: %+v", mine)
	}

	phone := e.signInAgain(recovery[0], "Phone/1.0")
	// A sign-in that stopped at the second factor is not a session: it opens nothing, and may not read the list either.
	half := e.client()
	expect(t, half.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}), 200, "")
	expect(t, half.do("GET", "/api/v1/auth/sessions", nil), 401, "mfa_required")

	mine = sessionItems(t, admin)
	if len(mine) != 2 || mine[0]["current"] != true || mine[1]["current"] != false || mine[1]["user_agent"] != "Phone/1.0" {
		t.Fatalf("this session first, then the phone: %+v", mine)
	}
	// From the phone the same two, with the phone as the current one.
	theirs := sessionItems(t, phone)
	if len(theirs) != 2 || theirs[0]["user_agent"] != "Phone/1.0" || theirs[0]["current"] != true || theirs[0]["id"] != mine[1]["id"] {
		t.Fatalf("the phone's view: %+v", theirs)
	}
	// The list names sessions by a reference: no cookie value and no CSRF token of any session is in it.
	raw := admin.do("GET", "/api/v1/auth/sessions", nil).Raw
	for _, c := range []*client{admin, phone} {
		if ck := c.cookie("nasa_session"); ck == nil || strings.Contains(raw, ck.Value) || strings.Contains(raw, c.csrf) {
			t.Fatalf("the list gives away a token: %s", raw)
		}
	}

	// Somebody else's session is not theirs to see or to end.
	viewer, _ := e.newUser(admin, "viewer1", "viewer")
	if own := sessionItems(t, viewer); len(own) != 1 || own[0]["current"] != true {
		t.Fatalf("a viewer sees their own session only: %+v", own)
	}
	phoneID := mine[1]["id"].(string)
	expect(t, viewer.do("DELETE", "/api/v1/auth/sessions/"+phoneID, nil), 404, "not_found")
	expect(t, phone.do("GET", "/api/v1/auth/me", nil), 200, "")

	// Not without the CSRF token, not something that is no reference, and not the session that asks.
	expect(t, admin.do("DELETE", "/api/v1/auth/sessions/"+phoneID, nil, "X-CSRF-Token", ""), 403, "csrf")
	expect(t, admin.do("DELETE", "/api/v1/auth/sessions/not-a-reference", nil), 404, "not_found")
	expect(t, admin.do("DELETE", "/api/v1/auth/sessions/"+mine[0]["id"].(string), nil), 409, "conflict")
	expect(t, admin.do("GET", "/api/v1/auth/me", nil), 200, "")
	expect(t, phone.do("GET", "/api/v1/auth/me", nil), 200, "")

	// Signing the phone out ends it there at once.
	expect(t, admin.do("DELETE", "/api/v1/auth/sessions/"+phoneID, nil), 204, "")
	expect(t, phone.do("GET", "/api/v1/auth/me", nil), 401, "unauthenticated")
	expect(t, admin.do("DELETE", "/api/v1/auth/sessions/"+phoneID, nil), 404, "not_found")
	if left := sessionItems(t, admin); len(left) != 1 || left[0]["current"] != true {
		t.Fatalf("after signing the phone out: %+v", left)
	}
	var who, agent string
	if err := e.st.Pool.QueryRow(t.Context(), `SELECT username, detail->>'user_agent' FROM audit_log WHERE action = 'session.revoke'`).Scan(&who, &agent); err != nil {
		t.Fatal(err)
	}
	if who != "admin" || agent != "Phone/1.0" {
		t.Errorf("audit entry: by %q, browser %q", who, agent)
	}
}

func TestSignOutEverywhereElse(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, recovery := e.setupAdmin()
	phone, laptop := e.signInAgain(recovery[0], "Phone/1.0"), e.signInAgain(recovery[1], "Laptop/1.0")
	half := e.client() // somebody who has the password and stopped at the second factor
	expect(t, half.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}), 200, "")
	viewer, _ := e.newUser(admin, "viewer1", "viewer")

	expect(t, admin.do("DELETE", "/api/v1/auth/sessions", nil, "X-CSRF-Token", ""), 403, "csrf")
	expect(t, phone.do("GET", "/api/v1/auth/me", nil), 200, "")

	r := admin.do("DELETE", "/api/v1/auth/sessions", nil)
	expect(t, r, 200, "")
	if r.Body["revoked"] != float64(3) {
		t.Fatalf("the phone, the laptop and the half sign-in: %s", r.Raw)
	}
	expect(t, phone.do("GET", "/api/v1/auth/me", nil), 401, "unauthenticated")
	expect(t, laptop.do("GET", "/api/v1/auth/me", nil), 401, "unauthenticated")
	expect(t, half.do("POST", "/api/v1/auth/mfa/recovery", map[string]string{"code": recovery[2]}), 401, "unauthenticated")
	// This session stays, and so does everybody else's.
	expect(t, admin.do("GET", "/api/v1/auth/me", nil), 200, "")
	expect(t, viewer.do("GET", "/api/v1/auth/me", nil), 200, "")
	if left := sessionItems(t, admin); len(left) != 1 {
		t.Fatalf("after signing out everywhere else: %+v", left)
	}
	var n string
	if err := e.st.Pool.QueryRow(t.Context(), `SELECT detail->>'sessions' FROM audit_log WHERE action = 'session.revoke_others'`).Scan(&n); err != nil || n != "3" {
		t.Errorf("audit entry: %q %v", n, err)
	}
	// Nothing left to end.
	if r := admin.do("DELETE", "/api/v1/auth/sessions", nil); r.Status != 200 || r.Body["revoked"] != float64(0) {
		t.Fatalf("a second time: %d %s", r.Status, r.Raw)
	}
}

// An administrator is told how the request arrived, so the interface can warn about a connection that is not encrypted. A viewer is
// not: it is not theirs to fix, and it describes the installation.
func TestSettingsTellAnAdministratorHowTheConnectionArrived(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	viewer, _ := e.newUser(admin, "viewer1", "viewer")

	r := admin.do("GET", "/api/v1/settings", nil)
	expect(t, r, 200, "")
	conn, _ := r.Body["connection"].(map[string]any)
	if conn == nil || conn["scheme"] != "http" || conn["require_https"] != false || conn["via_trusted_proxy"] != false {
		t.Fatalf("plain http, nothing enforced, no proxy: %s", r.Raw)
	}
	// A header from an address that is not a trusted proxy changes nothing: anybody can send it.
	r = admin.do("GET", "/api/v1/settings", nil, "X-Forwarded-Proto", "https")
	if conn, _ := r.Body["connection"].(map[string]any); conn == nil || conn["scheme"] != "http" {
		t.Fatalf("an untrusted X-Forwarded-Proto was believed: %s", r.Raw)
	}
	r = viewer.do("GET", "/api/v1/settings", nil)
	expect(t, r, 200, "")
	if _, has := r.Body["connection"]; has || r.Body["shares_root"] == nil {
		t.Fatalf("a viewer gets the settings without the connection: %s", r.Raw)
	}

	// Behind a proxy the app trusts, which says the browser used https.
	e2 := newAuthEnv(t, func(o *api.Options) { o.TrustedProxies = mustCIDR(t, "127.0.0.0/8"); o.RequireHTTPS = false })
	admin2, _, _ := e2.setupAdmin()
	r = admin2.do("GET", "/api/v1/settings", nil, "X-Forwarded-Proto", "https")
	expect(t, r, 200, "")
	if conn, _ := r.Body["connection"].(map[string]any); conn == nil || conn["scheme"] != "https" || conn["via_trusted_proxy"] != true || conn["require_https"] != false {
		t.Fatalf("https through a trusted proxy, not enforced: %s", r.Raw)
	}
}
