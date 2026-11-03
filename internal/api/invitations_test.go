package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

var ctxBG = context.Background()

func TestInvitationFlowOverHTTP(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()

	// Making the link: shown once; the token is in no later answer.
	r := admin.do("POST", "/api/v1/invitations", map[string]string{"username": "anna"})
	expect(t, r, 201, "")
	token := r.Body["token"].(string)
	if len(token) != 43 || r.Body["role"] != "viewer" || r.Body["kind"] != "new" || r.Body["created_by"] != "admin" {
		t.Errorf("created = %s", r.Raw)
	}
	if r.Body["url"] != "" {
		t.Errorf("with no public address configured the link is left to the interface: url = %v", r.Body["url"])
	}
	if list := admin.do("GET", "/api/v1/invitations", nil); strings.Contains(list.Raw, token) || len(list.Body["items"].([]any)) != 1 {
		t.Errorf("the list shows the token or the wrong rows: %s", list.Raw)
	}

	// The guest has no session; the token is the credential, in a header, never in the query string.
	guest := e.client()
	info := guest.do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", token)
	expect(t, info, 200, "")
	if info.Body["kind"] != "new" || info.Body["username"] != "anna" || info.Body["invited_by"] != "admin" || info.Body["role"] != "viewer" {
		t.Errorf("info = %s", info.Raw)
	}
	expect(t, guest.do("GET", "/api/v1/invitations/info?token="+token, nil), 404, "invalid_invitation")
	// An invitation is not a session.
	expect(t, e.client().do("GET", "/api/v1/shares", nil, "X-Invite-Token", token), 401, "unauthenticated")
	expect(t, e.client().do("GET", "/api/v1/shares", nil, "Authorization", "Bearer "+token), 401, "unauthenticated")

	// The guest chooses a password; whatever else they send (a role, an id) changes nothing: the role is the administrator's choice.
	g, rr := e.accept(token, "anna", pw)
	expect(t, rr, 200, "")
	if rr.Body["status"] != "enroll_required" {
		t.Errorf("accept = %s", rr.Raw)
	}
	expect(t, g.do("GET", "/api/v1/shares", nil), 403, "enroll_required") // a password alone is not enough
	e.enrol(g)
	expect(t, g.do("GET", "/api/v1/shares", nil), 200, "")
	if me := g.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any); me["role"] != "viewer" || me["username"] != "anna" {
		t.Errorf("me = %v", me)
	}
	// One use: the same link says the same thing as any bad token.
	for _, rep := range []reply{e.client().do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", token),
		e.client().do("POST", "/api/v1/invitations/password", map[string]string{"token": token, "username": "anna2", "password": pw})} {
		expect(t, rep, 404, "invalid_invitation")
	}
}

func TestEveryBadTokenAnswersTheSameOverHTTP(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	token, id := e.invite(admin, "", "viewer")
	tok2, id2 := e.invite(admin, "", "viewer")
	expect(t, admin.do("DELETE", fmt.Sprintf("/api/v1/invitations/%d", id2), nil), 204, "")
	if _, err := e.st.Pool.Exec(ctxBG, `UPDATE invitations SET expires_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	var answers []string
	for _, bad := range []string{token /*expired*/, tok2 /*revoked*/, strings.Repeat("A", 43) /*unknown*/, "", "short", strings.Repeat("x", 3000)} {
		r := e.client().do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", bad)
		expect(t, r, 404, "invalid_invitation")
		answers = append(answers, r.Body["title"].(string)+"|"+r.Body["detail"].(string))
	}
	for _, a := range answers[1:] {
		if a != answers[0] {
			t.Errorf("a bad token answered differently: %q vs %q (the route must not tell which tokens exist)", a, answers[0])
		}
	}
}

func TestAddressThatKeepsPresentingBadTokensIsLockedOut(t *testing.T) {
	e := newAuthEnv(t, nil)
	c := e.client()
	var locked bool
	for i := 0; i < 60 && !locked; i++ {
		r := c.do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", fmt.Sprintf("%043d", i))
		if r.Status == 429 {
			locked = true
			if r.code() != "locked" || r.Header.Get("Retry-After") == "" {
				t.Errorf("locked = %s", r.Raw)
			}
		} else {
			expect(t, r, 404, "invalid_invitation")
		}
	}
	if !locked {
		t.Error("60 bad tokens in a row did not lock the address, as failed sign-ins do")
	}
}

func TestInvitationLinkUsesTheConfiguredAddress(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) {
		o.PublicURL = "https://nas.example.com/"
		o.RP = auth.RelyingParty{ID: "other.example.com", Name: "NAS", Origins: []string{"https://other.example.com"}}
	})
	admin, _, _ := e.setupAdmin()
	r := admin.do("POST", "/api/v1/invitations", map[string]string{"role": "viewer"}, "Host", "10.0.0.5:8080", "X-Forwarded-Host", "internal.lan")
	expect(t, r, 201, "")
	// The configured public address wins; the request's own host (an internal address behind a proxy) is never used.
	if want := "https://nas.example.com/#invite=" + r.Body["token"].(string); r.Body["url"] != want {
		t.Errorf("url = %v, want %s", r.Body["url"], want)
	}
	// Without it, the first passkey origin; the token is in the fragment, which a browser never sends to a server.
	e2 := newAuthEnv(t, func(o *api.Options) {
		o.RP = auth.RelyingParty{ID: "other.example.com", Name: "NAS", Origins: []string{"https://other.example.com"}}
	})
	a2, _, _ := e2.setupAdmin()
	r = a2.do("POST", "/api/v1/invitations", map[string]string{"role": "viewer"})
	if want := "https://other.example.com/#invite=" + r.Body["token"].(string); r.Body["url"] != want {
		t.Errorf("url = %v, want %s", r.Body["url"], want)
	}
}

func TestResetOverHTTP(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	v, _ := e.newUser(admin, "viewer1", "viewer")
	vid := int64(v.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any)["id"].(float64))
	aid := int64(admin.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any)["id"].(float64))

	expect(t, admin.do("POST", fmt.Sprintf("/api/v1/users/%d/reset", aid), nil), 400, "") // not for yourself
	expect(t, admin.do("POST", "/api/v1/users/99999/reset", nil), 404, "not_found")
	expect(t, admin.do("POST", "/api/v1/users/abc/reset", nil), 400, "")
	r := admin.do("POST", fmt.Sprintf("/api/v1/users/%d/reset", vid), nil)
	expect(t, r, 201, "")
	token := r.Body["token"].(string)
	if r.Body["kind"] != "reset" || r.Body["username"] != "viewer1" {
		t.Errorf("reset = %s", r.Raw)
	}
	// Until the guest has finished, nothing changed: the user is still signed in and the old password works.
	expect(t, v.do("GET", "/api/v1/shares", nil), 200, "")
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw}), 200, "")
	info := e.client().do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", token)
	if info.Body["kind"] != "reset" || info.Body["username"] != "viewer1" {
		t.Errorf("info = %s", info.Raw)
	}
	// Finishing replaces the password, ends every session of that user and requires a second factor again.
	g, rr := e.accept(token, "somebody-else", "a-brand-new-password")
	expect(t, rr, 200, "")
	if rr.Body["status"] != "enroll_required" || rr.Body["user"].(map[string]any)["username"] != "viewer1" {
		t.Errorf("accept = %s", rr.Raw)
	}
	expect(t, v.do("GET", "/api/v1/shares", nil), 401, "unauthenticated")
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw}), 401, "invalid_credentials")
	e.enrol(g)
	expect(t, g.do("GET", "/api/v1/shares", nil), 200, "")
	// A second reset replaces an unused one.
	r1 := admin.do("POST", fmt.Sprintf("/api/v1/users/%d/reset", vid), nil)
	r2 := admin.do("POST", fmt.Sprintf("/api/v1/users/%d/reset", vid), nil)
	expect(t, e.client().do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", r1.Body["token"].(string)), 404, "invalid_invitation")
	expect(t, e.client().do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", r2.Body["token"].(string)), 200, "")
	// A disabled user cannot be reset until enabled.
	expect(t, admin.do("PATCH", fmt.Sprintf("/api/v1/users/%d", vid), map[string]any{"disabled": true}), 200, "")
	expect(t, admin.do("POST", fmt.Sprintf("/api/v1/users/%d/reset", vid), nil), 400, "")
}

func TestRevokeAndTheAuditTrailOfInvitations(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	token, id := e.invite(admin, "temp1", "viewer")
	expect(t, admin.do("DELETE", fmt.Sprintf("/api/v1/invitations/%d", id), nil), 204, "")
	expect(t, admin.do("DELETE", fmt.Sprintf("/api/v1/invitations/%d", id), nil), 404, "not_found")
	expect(t, admin.do("DELETE", "/api/v1/invitations/abc", nil), 400, "")
	expect(t, e.client().do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", token), 404, "invalid_invitation")
	if _, r := e.accept(token, "temp1", pw); r.Status != 404 {
		t.Errorf("a revoked invitation was accepted: %d", r.Status)
	}
	// The name is free again after a revoke.
	token2, _ := e.invite(admin, "temp1", "viewer")
	e.accept(token2, "temp1", pw)

	r := admin.do("GET", "/api/v1/audit?limit=100&action=invitation.", nil)
	expect(t, r, 200, "")
	seen := map[string]bool{}
	for _, it := range r.Body["items"].([]any) {
		seen[it.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"invitation.create", "invitation.revoke", "invitation.accept"} {
		if !seen[want] {
			t.Errorf("the audit log lacks %s; has %v", want, seen)
		}
	}
	for _, secret := range []string{token, token2, pw} {
		if strings.Contains(r.Raw, secret) {
			t.Error("a token or a password ended up in the audit log")
		}
	}
}

func TestInvitationOverHTTPNeedsHTTPSWhenRequired(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) { o.RequireHTTPS = true })
	c := e.client()
	for _, rep := range []reply{c.do("GET", "/api/v1/invitations/info", nil, "X-Invite-Token", strings.Repeat("a", 43)),
		c.do("POST", "/api/v1/invitations/password", map[string]string{"token": strings.Repeat("a", 43), "username": "x", "password": pw})} {
		expect(t, rep, 400, "https_required") // a password must not cross plain http
	}
}

func TestAdministratorsOnlyManageInvitations(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	v, _ := e.newUser(admin, "viewer1", "viewer")
	_, id := e.invite(admin, "", "viewer")
	anon := e.client()
	for _, call := range []func(*client) reply{
		func(c *client) reply { return c.do("GET", "/api/v1/invitations", nil) },
		func(c *client) reply { return c.do("POST", "/api/v1/invitations", map[string]string{"role": "viewer"}) },
		func(c *client) reply { return c.do("DELETE", fmt.Sprintf("/api/v1/invitations/%d", id), nil) },
		func(c *client) reply { return c.do("POST", "/api/v1/users/1/reset", nil) },
	} {
		expect(t, call(anon), 401, "unauthenticated")
		expect(t, call(v), 403, "forbidden")
	}
	// The routes that let an administrator choose someone's password no longer exist: not even for a signed-in administrator with a valid CSRF
	// token (a raw request, because the OpenAPI document does not know them either).
	for _, p := range []string{"/api/v1/users", "/api/v1/users/1/password"} {
		req, _ := http.NewRequest("POST", e.srv.URL+p, strings.NewReader(`{"username":"evil1","password":"`+pw+`","role":"admin"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", admin.csrf)
		resp, err := admin.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d: an administrator can still choose someone's password", p, resp.StatusCode)
		}
	}
	if users := admin.do("GET", "/api/v1/users", nil).Body["items"].([]any); len(users) != 2 {
		t.Errorf("%d users: an account was made without an invitation", len(users))
	}
}

// ---- passkey over HTTP ---------------------------------------------------------------------------------------------------------

func TestInvitationWithAPasskeyOverHTTP(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) {
		o.RP = auth.RelyingParty{ID: "nas.example.test", Name: "NAS", Origins: []string{"https://nas.example.test"}}
	})
	const origin = "https://nas.example.test"
	admin, _, _ := e.setupAdmin()
	token, _ := e.invite(admin, "", "viewer")
	hdr := []string{"X-Invite-Token", token}

	// A taken name is refused before a passkey is made; a bad token gets no options at all.
	g := e.client()
	expect(t, g.do("POST", "/api/v1/invitations/passkey/begin", map[string]string{"token": token, "username": "admin"}), 409, "conflict")
	expect(t, g.do("POST", "/api/v1/invitations/passkey/begin", map[string]string{"token": strings.Repeat("z", 43), "username": "x1y2z"}), 404, "invalid_invitation")

	r := g.do("POST", "/api/v1/invitations/passkey/begin", map[string]string{"token": token, "username": "pia"})
	expect(t, r, 200, "")
	ceremony := r.Body["ceremony"].(string)
	creation := rebuildCreation(t, r.Body["options"])
	va := ts.NewVirtualAuthenticator()
	body, _ := io.ReadAll(va.Create(creation, origin).Body)
	// The ceremony belongs to the token it was made for.
	other, _ := e.invite(admin, "", "viewer")
	expect(t, g.do("POST", "/api/v1/invitations/passkey/finish?ceremony="+ceremony+"&name=Laptop", json.RawMessage(body), "X-Invite-Token", other), 400, "")
	r = g.do("POST", "/api/v1/invitations/passkey/begin", map[string]string{"token": token, "username": "pia"})
	ceremony = r.Body["ceremony"].(string)
	creation = rebuildCreation(t, r.Body["options"])
	body, _ = io.ReadAll(va.Create(creation, origin).Body)
	r = g.do("POST", "/api/v1/invitations/passkey/finish?ceremony="+ceremony+"&name=Laptop", json.RawMessage(body), hdr...)
	expect(t, r, 200, "")
	if r.Body["status"] != "ok" || r.Body["user"].(map[string]any)["username"] != "pia" {
		t.Fatalf("finish = %s", r.Raw)
	}
	// A passkey verifies the user, so the guest is signed in completely, with no password anywhere.
	expect(t, g.do("GET", "/api/v1/shares", nil), 200, "")
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "pia", "password": pw}), 401, "invalid_credentials")
	expect(t, e.client().do("GET", "/api/v1/invitations/info", nil, hdr...), 404, "invalid_invitation")
}
