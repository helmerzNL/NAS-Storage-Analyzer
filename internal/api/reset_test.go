package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/pquerna/otp/totp"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
	"github.com/helmerznl/nas-storage-analyzer/internal/reset"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

const resetOrigin = "https://nas.example.test"

// next is the code of the step after the one the enrolment spent: a code works once, and the one used to enrol is gone.
func next(secret string, ahead int) string {
	c, _ := totp.GenerateCode(secret, time.Now().Add(time.Duration(ahead)*30*time.Second))
	return c
}

// logValue reads the value of key=value from a line of the text log, with or without quotes.
func logValue(t *testing.T, log, key string) string {
	t.Helper()
	i := strings.Index(log, key+"=")
	if i < 0 {
		t.Fatalf("%s is not in the log:\n%s", key, log)
	}
	v := log[i+len(key)+1:]
	if strings.HasPrefix(v, `"`) {
		return strings.SplitN(v[1:], `"`, 2)[0]
	}
	return strings.Fields(v)[0]
}

func (e *authEnv) rows(table string) int {
	e.t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// everythingIsStillThere is what a refused reset must leave: the accounts, the shares, the audit log.
func (e *authEnv) everythingIsStillThere(users, shares int) {
	e.t.Helper()
	if e.rows("users") != users || e.rows("shares") != shares || e.rows("audit_log") == 0 {
		e.t.Fatalf("a refused reset changed something: %d users, %d shares, %d audit rows", e.rows("users"), e.rows("shares"), e.rows("audit_log"))
	}
}

// populate fills an installation: a viewer, a share with an edited file type and a preference.
func (e *authEnv) populate(admin *client) {
	e.t.Helper()
	e.newUser(admin, "viewer1", "viewer")
	e.invite(admin, "guest", "viewer") // an invitation nobody has used yet
	expect(e.t, admin.do("POST", "/api/v1/shares", shareBody("media", e.t.TempDir())), 201, "")
	expect(e.t, admin.do("PUT", "/api/v1/extension-map/zzz", map[string]any{"category_id": 1}), 200, "")
	expect(e.t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"language": "nl"}), 200, "")
}

func TestResetRoutesAreForAdministratorsOnly(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	v, _ := e.newUser(admin, "viewer1", "viewer")
	calls := func(c *client) []reply {
		return []reply{
			c.do("GET", "/api/v1/system/reset", nil),
			c.do("POST", "/api/v1/system/reset/passkey", nil),
			c.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw}),
		}
	}
	for _, r := range calls(e.client()) {
		expect(t, r, 401, "unauthenticated")
	}
	for _, r := range calls(v) {
		expect(t, r, 403, "forbidden")
	}
	admin.csrf = ""
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw}), 403, "csrf")
	e.everythingIsStillThere(2, 0)
}

func TestResetRoutesAreAbsentWithoutAService(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) { o.Reset = nil })
	for _, p := range e.routes() {
		if strings.Contains(p, "/system/reset") {
			t.Errorf("registered without a service: %s", p)
		}
	}
}

func TestTheFormIsToldTheWordAndWhichProofsApply(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) {
		o.RP = auth.RelyingParty{ID: "nas.example.test", Name: "NAS", Origins: []string{resetOrigin}}
	})
	admin, _, _ := e.setupAdmin()
	r := admin.do("GET", "/api/v1/system/reset", nil)
	expect(t, r, 200, "")
	m := r.Body["methods"].(map[string]any)
	if r.Body["phrase"] != reset.Phrase || m["password_and_code"] != true || m["passkey"] != false || m["recovery"] != true || r.Body["scans_active"] != false {
		t.Fatalf("%s", r.Raw)
	}
	// With a passkey too, either way works.
	va := ts.NewVirtualAuthenticator()
	b := admin.do("POST", "/api/v1/auth/passkeys/register/begin", nil)
	reg := va.Create(rebuildCreation(t, b.Body["options"]), resetOrigin)
	body, _ := io.ReadAll(reg.Body)
	expect(t, admin.do("POST", "/api/v1/auth/passkeys/register/finish?ceremony="+b.Body["ceremony"].(string)+"&name=Laptop", json.RawMessage(body)), 200, "")
	if m := admin.do("GET", "/api/v1/system/reset", nil).Body["methods"].(map[string]any); m["passkey"] != true || m["password_and_code"] != true {
		t.Errorf("%v", m)
	}
}

func TestAResetNeedsTheWordAndAFreshProof(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, secret, _ := e.setupAdmin()
	e.populate(admin)
	users, shares := e.rows("users"), e.rows("shares")
	code := next(secret, 1)

	// The word: missing, wrong, in another case. A refused word is not a failed attempt to prove who is asking.
	for _, phrase := range []string{"", "reset", "RESET ", "RESETT", "yes"} {
		expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": phrase, "password": pw, "code": code}), 400, "reset_phrase")
	}
	if e.rows("login_failures") != 0 {
		t.Error("a wrong word counted as a failed login")
	}
	// A session alone is not enough, and neither is half a proof.
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET"}), 401, "reauth_required")
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw}), 401, "reauth_required")
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "code": code}), 401, "reauth_required")
	if e.rows("login_failures") != 0 {
		t.Error("an incomplete proof counted as a failed login")
	}
	// A wrong proof is refused, and counts.
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": "not-the-password", "code": code}), 401, "invalid_credentials")
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "code": "000000"}), 401, "invalid_code")
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "recovery_code": "AAAAAAAAAAAAAAAA"}), 401, "invalid_code")
	if e.rows("login_failures") == 0 {
		t.Error("wrong proofs were not counted")
	}
	e.everythingIsStillThere(users, shares)
}

// A reset form is not a second, unlimited place to guess the password: it is the same lockout as signing in.
func TestWrongProofsAtTheResetCountTowardTheSignInLockout(t *testing.T) {
	e := newAuthEnv(t, nil) // the test configuration locks after 4
	admin, secret, _ := e.setupAdmin()
	for i := 0; i < 4; i++ {
		expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": "guess-" + string(rune('a'+i)), "code": "000000"}), 401, "invalid_credentials")
	}
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "code": next(secret, 1)}), 429, "locked")
	// The same counter: the person is locked out of signing in too.
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}), 429, "locked")
	if e.rows("users") != 1 {
		t.Error("a locked-out reset wiped something")
	}
}

func TestAResetWithThePasswordAndACodeWipesEverythingAndStartsOver(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, secret, recovery := e.setupAdmin()
	e.populate(admin)
	cookie := admin.cookie("nasa_session").Value
	csrf := admin.csrf
	for _, n := range []string{"invitations", "audit_log", "sessions", "users", "shares"} {
		if e.rows(n) == 0 {
			t.Fatalf("the test did not fill %s", n)
		}
	}

	r := admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "code": next(secret, 1)})
	expect(t, r, 200, "")
	if r.Body["reset"] != true || r.Body["restarting"] != false || r.Body["setup_token_configured"] != false {
		t.Fatalf("%s", r.Raw)
	}
	if c := strings.Join(r.Header.Values("Set-Cookie"), "; "); !strings.Contains(c, "nasa_session=") || !strings.Contains(c, "Max-Age=0") {
		t.Errorf("the browser is not told to forget its session: %q", c)
	}

	// Every table of the schema is empty, except the record of migrations and the seed that is put back.
	tables, err := reset.Tables(context.Background(), e.st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	seeded := map[string]int{"categories": len(categorize.Categories), "extension_map": len(categorize.DefaultExtensions())}
	for _, tbl := range tables {
		if got := e.rows(tbl); got != seeded[tbl] {
			t.Errorf("%s has %d rows after a reset, want %d", tbl, got, seeded[tbl])
		}
	}

	// It is a new installation: the old session, password and second factor mean nothing, and nobody else is left.
	st := e.client().do("GET", "/api/v1/auth/status", nil)
	if st.Body["setup_required"] != true || st.Body["authenticated"] != false {
		t.Errorf("the application does not start over: %s", st.Raw)
	}
	old := e.client()
	old.csrf = csrf
	base, _ := url.Parse(e.srv.URL)
	old.hc.Jar.SetCookies(base, []*http.Cookie{{Name: "nasa_session", Value: cookie, Path: "/"}})
	expect(t, old.do("GET", "/api/v1/shares", nil), 401, "unauthenticated")
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}), 401, "invalid_credentials")
	expect(t, e.client().do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw}), 401, "invalid_credentials")
	_ = recovery

	// The old setup token means nothing (it was made up for the old installation); the new one is in the log, and a new owner can be set up.
	n := e.client()
	expect(t, n.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "owner", "password": pw}), 403, "invalid_setup_token")
	tok := logValue(t, e.logs.String(), "setup_token")
	expect(t, n.do("POST", "/api/v1/auth/setup", map[string]string{"token": tok, "username": "owner", "password": pw}), 200, "")
	if u := e.rows("users"); u != 1 {
		t.Errorf("%d users after the new setup", u)
	}
	// The file types are the defaults again, and a new share starts as share 1 with its own scan data.
	if n := e.rows(`extension_map WHERE ext = 'zzz'`); n != 0 {
		t.Error("an added file type survived")
	}
	expect(t, n.do("POST", "/api/v1/auth/totp/begin", nil), 200, "")
}

func TestTheResetLeavesOneRecordInTheLogAndNoSecret(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, secret, recovery := e.setupAdmin()
	e.populate(admin)
	cookie := admin.cookie("nasa_session").Value
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "code": next(secret, 1)}), 200, "")
	log := e.logs.String()
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "factory reset") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("%d records of the reset in the log:\n%s", len(lines), log)
	}
	l := lines[0]
	if !strings.Contains(l, "level=WARN") || !strings.Contains(l, "by=admin") || !strings.Contains(l, "users=2") || !strings.Contains(l, "shares=1") {
		t.Errorf("the record says too little: %s", l)
	}
	// Counts, never contents: no password, secret, code, session or token.
	for _, secretValue := range append([]string{pw, secret, cookie, admin.csrf, "viewer1", "media", "zzz"}, recovery...) {
		if secretValue != "" && strings.Contains(l, secretValue) {
			t.Errorf("the record of the reset contains %q", secretValue)
		}
	}
	for _, secretValue := range append([]string{pw, secret, cookie}, recovery...) {
		if strings.Contains(log, secretValue) {
			t.Errorf("the log contains a secret: %q", secretValue)
		}
	}
}

func TestAConfiguredSetupTokenStaysAfterAReset(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) { o.SetupTokenFixed = true })
	admin, secret, _ := e.setupAdmin()
	r := admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "code": next(secret, 1)})
	expect(t, r, 200, "")
	if r.Body["setup_token_configured"] != true {
		t.Errorf("%s", r.Raw)
	}
	if strings.Contains(e.logs.String(), "setup_token=") {
		t.Error("a configured token was written to the log")
	}
	expect(t, e.client().do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "owner", "password": pw}), 200, "")
}

func TestAResetWithARecoveryCode(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, recovery := e.setupAdmin()
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "recovery_code": recovery[0]}), 200, "")
	if e.rows("users") != 0 {
		t.Error("not wiped")
	}
}

func TestAResetWaitsForScansAndSpendsNothing(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, secret, _ := e.setupAdmin()
	expect(t, admin.do("POST", "/api/v1/shares", shareBody("media", t.TempDir())), 201, "")
	expect(t, admin.do("POST", "/api/v1/shares/1/scan", map[string]string{"mode": "auto"}), 202, "") // nobody works the queue in this test
	if r := admin.do("GET", "/api/v1/system/reset", nil); r.Body["scans_active"] != true {
		t.Errorf("the form is not told: %s", r.Raw)
	}
	code := next(secret, 1)
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "code": code}), 409, "busy")
	e.everythingIsStillThere(1, 1)
	// The code was not spent on a refusal that did not need it, so the same one works once the scan is gone.
	expect(t, admin.do("DELETE", "/api/v1/shares/1/scan", nil), 204, "")
	expect(t, admin.do("POST", "/api/v1/system/reset", map[string]string{"phrase": "RESET", "password": pw, "code": code}), 200, "")
}

func TestAResetWithAPasskey(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) {
		o.RP = auth.RelyingParty{ID: "nas.example.test", Name: "NAS", Origins: []string{resetOrigin}}
	})
	admin := e.client()
	expect(t, admin.do("POST", "/api/v1/auth/setup", map[string]string{"token": "setup-token-123", "username": "admin", "password": pw}), 200, "")
	va := ts.NewVirtualAuthenticator()
	b := admin.do("POST", "/api/v1/auth/passkeys/register/begin", nil)
	reg := va.Create(rebuildCreation(t, b.Body["options"]), resetOrigin)
	body, _ := io.ReadAll(reg.Body)
	expect(t, admin.do("POST", "/api/v1/auth/passkeys/register/finish?ceremony="+b.Body["ceremony"].(string)+"&name=Laptop", json.RawMessage(body)), 200, "")
	e.populate(admin)
	users, shares := e.rows("users"), e.rows("shares")

	challenge := func() (string, *protocol.CredentialAssertion) {
		r := admin.do("POST", "/api/v1/system/reset/passkey", nil)
		expect(t, r, 200, "")
		return r.Body["ceremony"].(string), rebuildAssertion(t, r.Body["options"])
	}
	answer := func(a *ts.VirtualAuthenticator, c *protocol.CredentialAssertion) json.RawMessage {
		ans := a.Get(c, resetOrigin)
		b, _ := io.ReadAll(ans.Body)
		return b
	}
	run := func(phrase, ceremony string, passkey json.RawMessage) reply {
		body := map[string]any{"phrase": phrase, "ceremony": ceremony}
		if passkey != nil {
			body["passkey"] = passkey
		}
		return admin.do("POST", "/api/v1/system/reset", body)
	}

	// The word is still needed, and a refused word does not use the challenge up.
	cer, ch := challenge()
	good := answer(va, ch)
	expect(t, run("nope", cer, good), 400, "reset_phrase")
	// A request without a challenge, or with one nobody issued, is refused.
	expect(t, run("RESET", "", good), 401, "reauth_required")
	// A passkey nobody registered signs the challenge: refused, and the challenge is spent, so even the right passkey cannot use it now.
	cer2, ch2 := challenge()
	stranger := ts.NewVirtualAuthenticator()
	stranger.UserHandle = va.UserHandle
	expect(t, run("RESET", cer2, answer(stranger, ch2)), 401, "invalid_code")
	expect(t, run("RESET", cer2, answer(va, ch2)), 401, "invalid_code")
	e.everythingIsStillThere(users, shares)
	// The passkey of somebody else (another account's, which the application knows) is refused too.
	viewer, _ := e.newUser(admin, "viewer2", "viewer")
	vp := ts.NewVirtualAuthenticator()
	rb := viewer.do("POST", "/api/v1/auth/passkeys/register/begin", nil)
	vreg := vp.Create(rebuildCreation(t, rb.Body["options"]), resetOrigin)
	vbody, _ := io.ReadAll(vreg.Body)
	expect(t, viewer.do("POST", "/api/v1/auth/passkeys/register/finish?ceremony="+rb.Body["ceremony"].(string)+"&name=V", json.RawMessage(vbody)), 200, "")
	cer3, ch3 := challenge()
	expect(t, run("RESET", cer3, answer(vp, ch3)), 401, "invalid_code")

	// The right passkey, a fresh challenge, the word: wiped.
	cer4, ch4 := challenge()
	r := run("RESET", cer4, answer(va, ch4))
	expect(t, r, 200, "")
	if e.rows("users") != 0 || e.rows("shares") != 0 || e.rows("webauthn_credentials") != 0 {
		t.Error("a reset with a passkey did not wipe")
	}
}
