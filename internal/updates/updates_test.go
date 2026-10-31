package updates

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub answers /<repo>/releases/latest the way GitHub does: a redirect to the tag page. mode picks what it says.
type fakeGitHub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	status  int
	loc     string
	delay   time.Duration
	hits    atomic.Int32
	headers []http.Header
	paths   []string
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{status: http.StatusFound, loc: "/" + DefaultRepo + "/releases/tag/v1.2.3"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.paths = append(f.paths, r.URL.RequestURI())
		status, loc, delay := f.status, f.loc, f.delay
		f.mu.Unlock()
		time.Sleep(delay)
		if loc != "" {
			w.Header().Set("Location", loc)
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			io.WriteString(w, "<html>not a redirect</html>")
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) set(status int, loc string) {
	f.mu.Lock()
	f.status, f.loc = status, loc
	f.mu.Unlock()
}

// clock is a controllable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func checker(f *fakeGitHub, current string) (*Checker, *clock) {
	clk := &clock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	c := New(current, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.BaseURL = f.srv.URL
	c.Now = clk.now
	return c, clk
}

func TestParseAndCompareNumerically(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"1.10.0", "1.9.0", true}, // numbers, not strings
		{"1.9.0", "1.10.0", false},
		{"2.1", "2.1.0", false}, // the shorter is padded with zeros
		{"2.1.0", "2.1", false},
		{"2.1.1", "2.1", true},
		{"v1.2.3", "1.2.2", true},  // a leading v is allowed on either side
		{"1.2.3", "v1.2.3", false}, // the same version is not an update
		{"0.2.0", "0.1.9", true},
		{"1.0.0-beta.1", "0.9.0", true}, // a suffix is ignored, the numbers are compared
		{"1.2.3", "dev", false},         // a build from a checkout is never "behind"
		{"", "1.0.0", false},
		{"1.0.0", "", false},
		{"latest", "1.0.0", false},
		{"1.x", "1.0.0", false}, // does not parse: not newer, never an error
		{"1.0.0", "beta", false},
	} {
		if got := Newer(tc.latest, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
	if _, ok := ParseVersion("1.2.-3"); ok {
		t.Error("a negative part parsed")
	}
}

func TestNewReleaseIsReportedAndTheSameVersionIsNotAnUpdate(t *testing.T) {
	f := newFake(t)
	c, _ := checker(f, "1.0.0")
	st := c.Status(context.Background())
	if !st.Enabled || st.Latest != "1.2.3" || !st.Available || st.Error != "" || st.CheckedAt == nil || st.Repo != DefaultRepo {
		t.Fatalf("status = %+v", st)
	}
	if want := f.srv.URL + "/" + DefaultRepo + "/releases/tag/v1.2.3"; st.URL != want {
		t.Errorf("url = %q, want %q", st.URL, want)
	}
	same, _ := checker(f, "1.2.3")
	if st := same.Status(context.Background()); st.Available || st.Latest != "1.2.3" {
		t.Errorf("the running version is not an update: %+v", st)
	}
	dev, _ := checker(f, "dev")
	if st := dev.Status(context.Background()); st.Available || st.Error != "" {
		t.Errorf("a dev build must not be told it is behind: %+v", st)
	}
	ahead, _ := checker(f, "9.0.0") // a beta that runs a newer number than the latest stable release
	if st := ahead.Status(context.Background()); st.Available {
		t.Errorf("a build ahead of the latest release has nothing to update to: %+v", st)
	}
}

func TestTheAnswerIsRememberedAndCheckNowKeepsTheFloor(t *testing.T) {
	f := newFake(t)
	c, clk := checker(f, "1.0.0")
	ctx := context.Background()
	c.Status(ctx)
	c.Status(ctx)
	clk.advance(5 * time.Hour)
	c.Status(ctx)
	if f.hits.Load() != 1 {
		t.Fatalf("GitHub was asked %d times within six hours, want 1", f.hits.Load())
	}
	clk.advance(2 * time.Hour) // six hours are over
	c.Status(ctx)
	if f.hits.Load() != 2 {
		t.Fatalf("after six hours GitHub was asked %d times, want 2", f.hits.Load())
	}
	c.Check(ctx) // "check now" straight after: inside the 30 second floor
	if f.hits.Load() != 2 {
		t.Error("check now went through inside the floor")
	}
	clk.advance(31 * time.Second)
	c.Check(ctx)
	if f.hits.Load() != 3 {
		t.Errorf("check now after the floor asked %d times, want 3", f.hits.Load())
	}
}

func TestOnlyOneRequestForConcurrentCallers(t *testing.T) {
	f := newFake(t)
	f.delay = 150 * time.Millisecond
	c, _ := checker(f, "1.0.0")
	var wg sync.WaitGroup
	answers := make([]Status, 20)
	for i := range answers {
		wg.Add(1)
		go func() { defer wg.Done(); answers[i] = c.Status(context.Background()) }()
	}
	wg.Wait()
	if f.hits.Load() != 1 {
		t.Errorf("20 concurrent callers made %d requests, want 1", f.hits.Load())
	}
	for i, st := range answers { // everyone waits for the one request and gets its answer, not an empty status
		if st.Latest != "1.2.3" || !st.Available {
			t.Fatalf("caller %d got %+v while the request was in flight", i, st)
		}
	}
}

func TestAFailureKeepsTheLastGoodAnswerAndIsRetriedAfterTenMinutes(t *testing.T) {
	f := newFake(t)
	c, clk := checker(f, "1.0.0")
	ctx := context.Background()
	if st := c.Status(ctx); !st.Available {
		t.Fatalf("setup: %+v", st)
	}
	f.set(http.StatusForbidden, "")
	clk.advance(7 * time.Hour)
	st := c.Status(ctx)
	if st.Error != "HTTP 403 (rate limit)" || st.ErrorDetail == "" {
		t.Errorf("the cause is not named: %+v", st)
	}
	if st.Latest != "1.2.3" || !st.Available || st.CheckedAt == nil {
		t.Errorf("a flaky network made the announced update disappear: %+v", st)
	}
	hits := f.hits.Load()
	clk.advance(5 * time.Minute)
	c.Status(ctx)
	if f.hits.Load() != hits {
		t.Error("a failure was retried within ten minutes")
	}
	f.set(http.StatusFound, "/"+DefaultRepo+"/releases/tag/v1.3.0")
	clk.advance(6 * time.Minute)
	st = c.Status(ctx)
	if st.Error != "" || st.Latest != "1.3.0" {
		t.Errorf("recovery: %+v", st)
	}
}

func TestUnexpectedAnswersAreErrorsAndNoReleaseIsNot(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		status int
		loc    string
		err    string // substring of Error; empty: no error
	}{
		{"no release yet: redirect to /releases", http.StatusFound, "/" + DefaultRepo + "/releases", ""},
		{"the repository is not visible", http.StatusNotFound, "", ""},
		{"a 200 instead of a redirect", http.StatusOK, "", "HTTP 200"},
		{"a server error", http.StatusInternalServerError, "", "HTTP 500"},
		{"rate limited", http.StatusTooManyRequests, "", "HTTP 429 (rate limit)"},
		{"redirect to another repository", http.StatusFound, "/evil/repo/releases/tag/v9.9.9", "unexpected answer"},
		{"redirect to another host's path", http.StatusFound, "https://evil.example/x", "unexpected answer"},
		{"redirect to something that is not a tag", http.StatusFound, "/" + DefaultRepo + "/releases/download/x", "unexpected answer"},
		{"redirect without a Location", http.StatusFound, "", "unexpected answer"},
		{"a tag that is not a version", http.StatusFound, "/" + DefaultRepo + "/releases/tag/latest", "unexpected answer"},
	} {
		f := newFake(t)
		f.set(tc.status, tc.loc)
		c, _ := checker(f, "1.0.0")
		st := c.Status(ctx)
		if tc.err == "" {
			if st.Error != "" || st.Latest != "" || st.Available || st.CheckedAt == nil {
				t.Errorf("%s: want no error and no release, got %+v", tc.name, st)
			}
			if st.URL != f.srv.URL+"/"+DefaultRepo+"/releases" {
				t.Errorf("%s: the link should be the releases page, got %q", tc.name, st.URL)
			}
		} else if !strings.Contains(st.Error, tc.err) || st.Available {
			t.Errorf("%s: want error %q, got %+v", tc.name, tc.err, st)
		}
	}
}

func TestTheLinkIsBuiltFromTheRepositoryAndNeverFromTheAnswer(t *testing.T) {
	f := newFake(t)
	// An absolute Location on another host with a path that looks right: only the path is read, the host is ignored.
	f.set(http.StatusFound, "https://evil.example/"+DefaultRepo+"/releases/tag/v2.0.0")
	c, _ := checker(f, "1.0.0")
	st := c.Status(context.Background())
	if st.Latest != "2.0.0" {
		t.Fatalf("latest = %q", st.Latest)
	}
	if strings.Contains(st.URL, "evil.example") || st.URL != f.srv.URL+"/"+DefaultRepo+"/releases/tag/v2.0.0" {
		t.Errorf("the link was taken from the answer: %q", st.URL)
	}
}

func TestItSendsNothingAboutTheInstallation(t *testing.T) {
	f := newFake(t)
	c, _ := checker(f, "1.0.0")
	c.Status(context.Background())
	if len(f.headers) != 1 {
		t.Fatalf("%d requests", len(f.headers))
	}
	h := f.headers[0]
	if h.Get("User-Agent") != UserAgent {
		t.Errorf("User-Agent = %q", h.Get("User-Agent"))
	}
	for name := range h {
		switch http.CanonicalHeaderKey(name) {
		case "User-Agent", "Accept-Encoding": // Go's transport adds the second one
		default:
			t.Errorf("the request carries the header %s: %v", name, h[name])
		}
	}
	if want := "/" + DefaultRepo + "/releases/latest"; f.paths[0] != want {
		t.Errorf("request URI = %q, want exactly %q (no query, no version)", f.paths[0], want)
	}
	if strings.Contains(strings.ToLower(h.Get("User-Agent")), "1.0.0") {
		t.Error("the version is in the User-Agent")
	}
}

func TestDisabledMeansNoRequestAndNoError(t *testing.T) {
	f := newFake(t)
	c, _ := checker(f, "1.0.0")
	c.Enabled = false
	for _, st := range []Status{c.Status(context.Background()), c.Check(context.Background())} {
		if st.Enabled || st.Error != "" || st.Available || st.Latest != "" || st.Current != "1.0.0" {
			t.Errorf("status = %+v", st)
		}
	}
	if f.hits.Load() != 0 {
		t.Errorf("a disabled check made %d requests", f.hits.Load())
	}
}

func TestUnreachableAndSlowGitHubAreErrorsNotCrashes(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _ := checker(f, "1.0.0")
	f.srv.Close() // connection refused
	st := c.Status(ctx)
	if st.Error != "Connection error (no connection or DNS)" || st.Available {
		t.Errorf("unreachable: %+v", st)
	}

	slow := newFake(t)
	slow.delay = 400 * time.Millisecond
	c2, _ := checker(slow, "1.0.0")
	c2.Client = &http.Client{Timeout: 50 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if st := c2.Status(ctx); st.Error != "Timeout" {
		t.Errorf("slow: %+v", st)
	}
}

func TestACertificateProblemIsNamed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	c := New("1.0.0", true, nil)
	c.BaseURL = srv.URL // a self-signed certificate the default client does not trust
	if st := c.Status(context.Background()); st.Error != "TLS error (certificate)" {
		t.Errorf("status = %+v", st)
	}
}
