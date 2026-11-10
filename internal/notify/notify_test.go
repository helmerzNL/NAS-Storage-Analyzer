package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/notify"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

var ctx = context.Background()

func newService(t *testing.T) (*notify.Service, *store.Store) {
	t.Helper()
	st := ts.NewStore(t)
	box, err := secrets.New(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := notify.NewService(st.Pool, box)
	s.SetRetryWaits(0, 0, 0)
	return s, st
}

// receiver is a webhook: it keeps what it was sent and answers with the status it was told to.
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    []received
}

type received struct {
	method, path, ctype, title, priority, tags, agent string
	body                                              []byte
}

func newReceiver(t *testing.T, status int) *receiver {
	t.Helper()
	r := &receiver{status: status}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, received{req.Method, req.URL.RequestURI(), req.Header.Get("Content-Type"), req.Header.Get("Title"), req.Header.Get("Priority"),
			req.Header.Get("Tags"), req.Header.Get("User-Agent"), body})
		status := r.status
		r.mu.Unlock()
		if status >= 300 && status < 400 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
		io.WriteString(w, "the answer of the receiver, which must never be shown")
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) all() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

func target(name, url string) notify.Input {
	in := notify.Defaults()
	in.Name, in.URL = name, &url
	return in
}

func mustCreate(t *testing.T, s *notify.Service, in notify.Input) *notify.Target {
	t.Helper()
	tg, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestTargetsAreStoredSealedAndShownMasked(t *testing.T) {
	s, st := newService(t)
	const address = "https://user:pass@ntfy.example.org:8443/my-secret-topic?auth=tk_abcdef"
	tg := mustCreate(t, s, target("  Phone ", address))
	if tg.Name != "Phone" || tg.URLHint != "https://ntfy.example.org:8443/…" || tg.Format != notify.FormatJSON || !tg.Enabled ||
		tg.Events != (notify.Events{Failed: true, Empty: true, Errors: true}) || tg.LastAt != nil {
		t.Fatalf("a new target: %+v", tg)
	}
	// What leaves the service never has the address: not the path, not the query, not the user.
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	out, _ := json.Marshal(list)
	for _, secret := range []string{"my-secret-topic", "tk_abcdef", "user", "pass"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("the list gives away %q: %s", secret, out)
		}
	}
	// In the database it is sealed, not readable text.
	var enc []byte
	if err := st.Pool.QueryRow(ctx, `SELECT url_enc FROM notification_targets`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("ntfy.example.org")) || bytes.Contains(enc, []byte("my-secret-topic")) {
		t.Error("the address is stored readable")
	}

	// Changing something else keeps the address; a second target cannot take the name.
	keep := notify.Defaults()
	keep.Name, keep.Format, keep.Lang, keep.Enabled, keep.Events = "Phone", notify.FormatText, "nl", false, notify.Events{Finished: true}
	up, err := s.Update(ctx, tg.ID, keep)
	if err != nil || up.URLHint != tg.URLHint || up.Format != notify.FormatText || up.Lang != "nl" || up.Enabled || up.Events != (notify.Events{Finished: true}) {
		t.Fatalf("update without an address: %+v %v", up, err)
	}
	if _, err := s.Create(ctx, target("Phone", "https://other.example/x")); !errors.Is(err, notify.ErrConflict) {
		t.Errorf("a second target with the same name: %v", err)
	}
	other := mustCreate(t, s, target("Other", "https://other.example/x"))
	taken := notify.Defaults()
	taken.Name = "Phone"
	if _, err := s.Update(ctx, other.ID, taken); !errors.Is(err, notify.ErrConflict) {
		t.Errorf("renaming to a name that is taken: %v", err)
	}
	if _, err := s.Update(ctx, 9999, keep); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("updating a target that is not there: %v", err)
	}
	if name, err := s.Delete(ctx, other.ID); err != nil || name != "Other" {
		t.Errorf("delete: %q %v", name, err)
	}
	if _, err := s.Delete(ctx, other.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestWhatATargetMayBe(t *testing.T) {
	s, _ := newService(t)
	field := func(in notify.Input) string {
		_, err := s.Create(ctx, in)
		var ve *notify.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%+v: expected a validation error, got %v", in, err)
		}
		return ve.Field
	}
	for _, bad := range []string{"", "ntfy.sh/topic", "ftp://host/x", "file:///etc/passwd", "javascript:alert(1)", "https://", "https://host/x#fragment",
		"https://host/a b", "https://host/x\nHost: other", "gopher://host/", "//host/x", "https://" + strings.Repeat("a", 2000)} {
		if f := field(target("T", bad)); f != "url" {
			t.Errorf("address %q was refused for %q", bad, f)
		}
	}
	in := notify.Defaults()
	in.Name = "No address"
	if f := field(in); f != "url" {
		t.Errorf("a new target without an address: %s", f)
	}
	for _, c := range []struct {
		mut  func(*notify.Input)
		want string
	}{
		{func(i *notify.Input) { i.Name = "   " }, "name"},
		{func(i *notify.Input) { i.Name = strings.Repeat("n", 61) }, "name"},
		{func(i *notify.Input) { i.Format = "xml" }, "format"},
		{func(i *notify.Input) { i.Lang = "de" }, "lang"},
	} {
		in := target("T", "https://host/x")
		c.mut(&in)
		if f := field(in); f != c.want {
			t.Errorf("refused for %q, expected %q", f, c.want)
		}
	}
	// At most five.
	for i := 0; i < notify.MaxTargets; i++ {
		mustCreate(t, s, target(string(rune('A'+i)), "https://host/x"))
	}
	if _, err := s.Create(ctx, target("One too many", "https://host/x")); !errors.Is(err, notify.ErrTooMany) {
		t.Errorf("a sixth target: %v", err)
	}
}

func scanFailed() notify.Event {
	return notify.Event{Kind: notify.KindFailed, Share: "Media", ShareID: 3, ScanID: 12, JobID: 34, Trigger: "schedule",
		Error: "share path /mnt/shares/media: stat /mnt/shares/media: no such file or directory\n(is the share mounted into the container?)",
		At:    time.Date(2026, 11, 10, 3, 0, 5, 123, time.UTC)}
}

func TestAJSONTargetGetsTheWholeMessage(t *testing.T) {
	s, _ := newService(t)
	r := newReceiver(t, 200)
	in := target("Home Assistant", r.URL+"/api/webhook/abc123")
	in.Lang = "nl"
	tg := mustCreate(t, s, in)

	ds, err := s.Notify(ctx, scanFailed())
	if err != nil || len(ds) != 1 || !ds[0].OK || ds[0].Status != 200 || ds[0].Attempts != 1 || ds[0].Error != "" || ds[0].Target != "Home Assistant" || ds[0].TargetID != tg.ID {
		t.Fatalf("delivery: %+v %v", ds, err)
	}
	got := r.all()
	if len(got) != 1 || got[0].method != "POST" || got[0].path != "/api/webhook/abc123" || got[0].ctype != "application/json" || !strings.HasPrefix(got[0].agent, "NASSA") {
		t.Fatalf("the request: %+v", got)
	}
	var m map[string]any
	if err := json.Unmarshal(got[0].body, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"app": "NASSA", "event": "scan.failed", "title": "NASSA: scan van Media mislukt", "priority": float64(8), "share": "Media",
		"share_id": float64(3), "scan_id": float64(12), "job_id": float64(34), "trigger": "schedule", "at": "2026-11-10T03:00:05Z"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	msg, _ := m["message"].(string)
	if !strings.HasPrefix(msg, "De scan van Media is mislukt: share path /mnt/shares/media") || strings.Contains(msg, "\n") || m["error"] == "" {
		t.Errorf("the message is one line with the worker's reason: %q", msg)
	}
	// A failed scan has no counts to give; they are left out, not sent as zero.
	for _, k := range []string{"files", "dirs", "bytes", "errors"} {
		if _, has := m[k]; has {
			t.Errorf("a failed scan sent %s", k)
		}
	}
	// The outcome is kept for the settings screen.
	list, _ := s.List(ctx)
	if list[0].LastAt == nil || list[0].LastOK == nil || !*list[0].LastOK || list[0].LastStatus == nil || *list[0].LastStatus != 200 || list[0].LastError != nil {
		t.Errorf("the recorded outcome: %+v", list[0])
	}
}

func TestATextTargetGetsTheMessageWithHeadersForNtfy(t *testing.T) {
	s, _ := newService(t)
	r := newReceiver(t, 200)
	in := target("ntfy", r.URL+"/nas-alerts")
	in.Format, in.Events = notify.FormatText, notify.Events{Errors: true, Finished: true}
	mustCreate(t, s, in)

	if ds, _ := s.Notify(ctx, notify.Event{Kind: notify.KindErrors, Share: "Média", Errors: 7, Files: 100, Dirs: 9, Bytes: 5 << 30, At: time.Now()}); len(ds) != 1 || !ds[0].OK {
		t.Fatalf("delivery: %+v", ds)
	}
	if ds, _ := s.Notify(ctx, notify.Event{Kind: notify.KindFinished, Share: "Média", Files: 100, Dirs: 9, Bytes: 5 << 30, At: time.Now()}); len(ds) != 1 || !ds[0].OK {
		t.Fatalf("delivery: %+v", ds)
	}
	// It did not ask for failed scans.
	if ds, _ := s.Notify(ctx, scanFailed()); len(ds) != 0 {
		t.Fatalf("a kind the target did not ask for was sent: %+v", ds)
	}
	got := r.all()
	if len(got) != 2 {
		t.Fatalf("requests: %+v", got)
	}
	if got[0].ctype != "text/plain; charset=utf-8" || got[0].title != "NASSA: scan of Média could not read everything" || got[0].priority != "3" || got[0].tags != "warning" ||
		string(got[0].body) != "The scan of Média finished, but 7 folders or files could not be read. The audit log names the folders." {
		t.Errorf("the message about unreadable folders: %+v body %q", got[0], got[0].body)
	}
	if got[1].title != "NASSA: scan of Média finished" || got[1].priority != "2" || got[1].tags != "white_check_mark" ||
		string(got[1].body) != "The scan of Média finished: 100 files in 9 folders, 5.0 GiB." {
		t.Errorf("the message about a finished scan: %+v body %q", got[1], got[1].body)
	}
}

func TestOnlyEnabledTargetsThatAskedGetAMessage(t *testing.T) {
	s, _ := newService(t)
	wants, off, other := newReceiver(t, 204), newReceiver(t, 200), newReceiver(t, 200)
	mustCreate(t, s, target("Wants it", wants.URL+"/hook"))
	disabled := target("Switched off", off.URL+"/hook")
	disabled.Enabled = false
	offTarget := mustCreate(t, s, disabled)
	quiet := target("Only finished scans", other.URL+"/hook")
	quiet.Events = notify.Events{Finished: true}
	mustCreate(t, s, quiet)

	ds, err := s.Notify(ctx, notify.Event{Kind: notify.KindEmpty, Share: "Media", At: time.Now()})
	if err != nil || len(ds) != 1 || ds[0].Target != "Wants it" || !ds[0].OK || ds[0].Status != 204 {
		t.Fatalf("deliveries: %+v %v", ds, err)
	}
	if len(wants.all()) != 1 || len(off.all()) != 0 || len(other.all()) != 0 {
		t.Fatalf("who was called: %d %d %d", len(wants.all()), len(off.all()), len(other.all()))
	}
	// An event of no known kind goes nowhere.
	if ds, _ := s.Notify(ctx, notify.Event{Kind: "share.create", Share: "Media"}); len(ds) != 0 {
		t.Errorf("an unknown kind was sent: %+v", ds)
	}
	// The Test button reaches a target that is switched off: that is how one finds out whether it works.
	d, err := s.Test(ctx, offTarget.ID)
	if err != nil || !d.OK || len(off.all()) != 1 {
		t.Fatalf("test of a disabled target: %+v %v", d, err)
	}
	var m map[string]any
	json.Unmarshal(off.all()[0].body, &m)
	if m["event"] != "test" || m["title"] != "NASSA: test message" || m["share"] != nil {
		t.Errorf("the test message: %v", m)
	}
	if _, err := s.Test(ctx, 9999); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("testing a target that is not there: %v", err)
	}
}

func TestAFailedDeliveryIsTriedAgainOnlyWhenThatCanHelp(t *testing.T) {
	s, _ := newService(t)
	down, refused, moved := newReceiver(t, 503), newReceiver(t, 401), newReceiver(t, 302)
	mustCreate(t, s, target("a down", down.URL+"/secret-token-1"))
	mustCreate(t, s, target("b refused", refused.URL+"/secret-token-2"))
	mustCreate(t, s, target("c moved", moved.URL+"/secret-token-3"))

	ds, err := s.Notify(ctx, scanFailed())
	if err != nil || len(ds) != 3 {
		t.Fatalf("deliveries: %+v %v", ds, err)
	}
	// A server error may pass: three attempts. A refusal and a redirect will not: one.
	if ds[0].OK || ds[0].Attempts != 3 || ds[0].Status != 503 || len(down.all()) != 3 {
		t.Errorf("a receiver that is down: %+v (%d requests)", ds[0], len(down.all()))
	}
	if ds[1].OK || ds[1].Attempts != 1 || ds[1].Status != 401 || !strings.Contains(ds[1].Error, "token") || len(refused.all()) != 1 {
		t.Errorf("a receiver that refuses: %+v", ds[1])
	}
	// The redirect is not followed: the receiver was asked once, and /elsewhere never.
	if ds[2].OK || ds[2].Attempts != 1 || ds[2].Status != 302 || !strings.Contains(ds[2].Error, "redirect") || len(moved.all()) != 1 || moved.all()[0].path != "/secret-token-3" {
		t.Errorf("a receiver that redirects: %+v %+v", ds[2], moved.all())
	}
	list, _ := s.List(ctx)
	for i, tg := range list {
		if tg.LastOK == nil || *tg.LastOK || tg.LastError == nil || tg.LastStatus == nil {
			t.Errorf("the recorded outcome of %q: %+v", tg.Name, tg)
		}
		// Neither the delivery nor what is kept of it has the address or the receiver's answer.
		for _, text := range []string{ds[i].Error, *tg.LastError} {
			if strings.Contains(text, "secret-token") || strings.Contains(text, "127.0.0.1") && strings.Contains(text, "/secret") || strings.Contains(text, "must never be shown") {
				t.Errorf("%q leaks: %s", tg.Name, text)
			}
		}
	}
	// It comes back: the next message goes through and the stored outcome says so.
	down.mu.Lock()
	down.status = 200
	down.mu.Unlock()
	if ds, _ := s.Notify(ctx, scanFailed()); !ds[0].OK || ds[0].Attempts != 1 {
		t.Errorf("after the receiver came back: %+v", ds[0])
	}
	if list, _ := s.List(ctx); list[0].LastOK == nil || !*list[0].LastOK || list[0].LastError != nil {
		t.Errorf("the stored outcome after a success: %+v", list[0])
	}
}

func TestNothingAnswersAndTheAddressStaysOutOfTheError(t *testing.T) {
	s, _ := newService(t)
	gone := httptest.NewServer(http.NotFoundHandler())
	address := gone.URL + "/topic-with-a-secret-token?auth=tk_secret"
	gone.Close() // nothing listens there any more
	mustCreate(t, s, target("Gone", address))
	ds, err := s.Notify(ctx, scanFailed())
	if err != nil || len(ds) != 1 || ds[0].OK || ds[0].Status != 0 || ds[0].Attempts != 3 || ds[0].Error == "" {
		t.Fatalf("delivery: %+v %v", ds, err)
	}
	if strings.Contains(ds[0].Error, "secret") || strings.Contains(ds[0].Error, "tk_") {
		t.Errorf("the error has the address in it: %s", ds[0].Error)
	}
}

// The server makes the request, so an address that leads to where a cloud machine keeps its credentials is refused before anything
// is sent, also when it is a name that resolves there.
func TestAddressesTheServerMustNotCallAreRefused(t *testing.T) {
	s, _ := newService(t)
	for name, address := range map[string]string{
		"metadata": "http://169.254.169.254/latest/meta-data/", "unspecified": "http://0.0.0.0:9/hook", "multicast": "http://224.0.0.1/hook",
	} {
		tg := mustCreate(t, s, target(name, address))
		d, err := s.Test(ctx, tg.ID)
		if err != nil || d.OK || d.Status != 0 || !strings.Contains(d.Error, "refused: the address resolves to") {
			t.Errorf("%s (%s): %+v %v", name, address, d, err)
		}
	}
	// The check itself, on the address about to be dialled (after a name was resolved), for both families: a machine without IPv6
	// cannot try the second through a socket.
	for address, refused := range map[string]bool{
		"169.254.169.254:80": true, "169.254.0.1:443": true, "[fe80::1]:80": true, "[fe80::1%eth0]:80": true, "0.0.0.0:80": true, "[::]:80": true,
		"224.0.0.1:80": true, "[ff02::1]:80": true,
		// Where Home Assistant, Gotify and ntfy live: this machine, the home network, the internet.
		"127.0.0.1:8123": false, "[::1]:8123": false, "192.168.1.10:8123": false, "10.0.0.5:80": false, "172.17.0.1:80": false, "[fd00::10]:80": false,
		"93.184.216.34:443": false, "[2606:2800:220:1:248:1893:25c8:1946]:443": false,
	} {
		if err := notify.GuardDial("tcp", address, nil); (err != nil) != refused {
			t.Errorf("dialling %s: refused %v, expected %v (%v)", address, err != nil, refused, err)
		}
	}
}

func TestAnAddressThatDoesNotOpenIsSaid(t *testing.T) {
	s, st := newService(t)
	r := newReceiver(t, 200)
	tg := mustCreate(t, s, target("Sealed with another key", r.URL+"/hook"))
	other, _ := secrets.New(bytes.Repeat([]byte{5}, secrets.KeySize))
	if _, err := st.Pool.Exec(ctx, `UPDATE notification_targets SET url_enc = $1 WHERE id = $2`, other.Seal([]byte(r.URL+"/hook")), tg.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].URLHint != "" {
		t.Fatalf("the list still works and shows no address: %+v %v", list, err)
	}
	d, err := s.Test(ctx, tg.ID)
	if err != nil || d.OK || !strings.Contains(d.Error, "NASSA_ENCRYPTION_KEY") || len(r.all()) != 0 {
		t.Fatalf("a target whose address does not open: %+v %v", d, err)
	}
}
