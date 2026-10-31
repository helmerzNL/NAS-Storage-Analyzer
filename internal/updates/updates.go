// Package updates tells the administrator that a newer release exists. It does not update anything.
//
// It follows App-Guidance's update-check.md: the app asks GitHub for the latest published, non-prerelease release of its own repository
// and compares it with the version it runs. It reads one thing, sends nothing about the installation (a fixed User-Agent, no version, no
// instance id, no user data), can be switched off, and never turns a GitHub outage into an error page.
//
// It does not use api.github.com: anonymous requests to the API are limited to 60 an hour per IP address, and a server that shares an
// address got "HTTP 403 (rate limit)" for something that had worked in testing. It requests /<repo>/releases/latest WITHOUT following the
// redirect and reads the tag from the Location header (/releases/tag/v1.2.3), which has no such budget.
package updates

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultRepo is the repository whose releases are announced.
	DefaultRepo = "helmerzNL/NAS-Storage-Analyzer"
	// DefaultBase is where releases are looked up. NASA_UPDATE_BASE_URL may point it elsewhere (a mirror, or a test double).
	DefaultBase = "https://github.com"

	// UserAgent is the only thing that identifies the request, and it is the same for every installation.
	UserAgent = "nas-storage-analyzer-update-check"

	// SuccessTTL is how long a good answer is remembered; FailureTTL how long a failure is, before the next try.
	SuccessTTL = 6 * time.Hour
	FailureTTL = 10 * time.Minute
	// ForceFloor is the shortest time between two real requests, also for "check now", so it cannot be used to hammer GitHub.
	ForceFloor = 30 * time.Second
	// Timeout bounds one request.
	Timeout = 5 * time.Second
)

// Status is what the interface reads (GET /api/v1/system/update).
type Status struct {
	// Enabled is false when the check is switched off (NASA_UPDATE_CHECK=false): reported as off, not as a failure.
	Enabled bool `json:"enabled"`
	// Current is the version this process runs ("dev" for a build from a checkout).
	Current string `json:"current"`
	// Latest is the newest published release, without the v; empty when there is none (or none was found yet).
	Latest string `json:"latest"`
	// Available is true when Latest is newer than Current.
	Available bool `json:"available"`
	// URL is the page of the release (or the releases page when there is none), built from the repository and the version, never taken from
	// the answer, so the interface cannot be made to point somewhere else.
	URL string `json:"url"`
	// CheckedAt is when GitHub was last asked successfully; nil when never.
	CheckedAt *time.Time `json:"checked_at"`
	// Error is the cause in words an administrator can act on ("HTTP 403 (rate limit)"); empty when the last attempt worked.
	Error string `json:"error"`
	// ErrorDetail is the raw reason, for the log and for support.
	ErrorDetail string `json:"error_detail"`
	// Repo is the repository that was asked.
	Repo string `json:"repo"`
}

// Checker asks GitHub, remembers the answer and shares one request between concurrent callers.
type Checker struct {
	Repo    string // owner/name
	Current string
	Enabled bool
	BaseURL string // default DefaultBase
	Client  *http.Client
	Log     *slog.Logger
	Now     func() time.Time // tests

	mu       sync.Mutex
	good     *Status   // the last successful answer; a failure keeps it
	checked  time.Time // when the last good answer was fetched
	lastTry  time.Time // when GitHub was last asked, whatever came back
	lastFail time.Time // when the last failure was (zero after a success)
	failErr  string
	failDet  string
}

// New returns a Checker for the app's own repository.
func New(current string, enabled bool, log *slog.Logger) *Checker {
	return &Checker{Repo: DefaultRepo, Current: current, Enabled: enabled, BaseURL: DefaultBase, Log: log}
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{
		Timeout: Timeout,
		// The redirect is the answer: do not follow it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Status returns the remembered answer, asking GitHub only when it is too old. A page view never calls GitHub twice in six hours.
func (c *Checker) Status(ctx context.Context) Status { return c.status(ctx, false) }

// Check asks again now, unless GitHub was asked less than ForceFloor ago.
func (c *Checker) Check(ctx context.Context) Status { return c.status(ctx, true) }

func (c *Checker) status(ctx context.Context, force bool) Status {
	if !c.Enabled {
		return Status{Enabled: false, Current: c.Current, URL: c.releasesURL(), Repo: c.Repo}
	}
	c.mu.Lock() // one lock around the fetch: concurrent callers make one request
	defer c.mu.Unlock()
	now := c.now()
	if c.shouldAsk(now, force) {
		c.lastTry = now
		latest, err := c.fetch(ctx)
		if err != nil {
			c.lastFail = now
			c.failErr, c.failDet = cause(err)
			if c.Log != nil {
				c.Log.Warn("update check failed", "cause", c.failErr, "detail", c.failDet)
			}
		} else {
			c.lastFail, c.failErr, c.failDet = time.Time{}, "", ""
			c.checked = now
			c.good = &Status{Latest: latest}
		}
	}
	return c.snapshot()
}

func (c *Checker) shouldAsk(now time.Time, force bool) bool {
	if !c.lastTry.IsZero() && now.Sub(c.lastTry) < ForceFloor {
		return false // never twice within the floor, for anyone
	}
	if force {
		return true
	}
	switch {
	case c.good == nil && c.lastFail.IsZero():
		return true // never asked
	case !c.lastFail.IsZero():
		return now.Sub(c.lastFail) >= FailureTTL
	default:
		return now.Sub(c.checked) >= SuccessTTL
	}
}

// snapshot builds the status from what is remembered: a failure keeps the last good answer and adds the error.
func (c *Checker) snapshot() Status {
	st := Status{Enabled: true, Current: c.Current, Repo: c.Repo, Error: c.failErr, ErrorDetail: c.failDet}
	if c.good != nil {
		st.Latest = c.good.Latest
		t := c.checked
		st.CheckedAt = &t
	}
	st.URL = c.releaseURL(st.Latest)
	st.Available = st.Latest != "" && Newer(st.Latest, c.Current)
	return st
}

func (c *Checker) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBase
}

func (c *Checker) releasesURL() string { return c.base() + "/" + c.Repo + "/releases" }

// releaseURL is the page of a release, built from the repository and the version.
func (c *Checker) releaseURL(version string) string {
	if version == "" {
		return c.releasesURL()
	}
	return c.releasesURL() + "/tag/v" + version
}

var tagRE = regexp.MustCompile(`/releases/tag/v?([0-9][0-9A-Za-z.+-]*)$`)

// errUnexpected is an answer GitHub should not give: GitHub changed, or the network lied.
type errUnexpected struct{ what string }

func (e errUnexpected) Error() string { return e.what }

// fetch asks for the latest release. "" with no error means there is none: the repository has no release yet, or is not visible.
func (c *Checker) fetch(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout+time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.releasesURL()+"/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent) // the only identification, the same for every installation
	resp, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", nil // the repository is not visible: no release, no error
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || resp.Header.Get("Location") == "" {
			return "", errUnexpected{"redirect without a usable Location"}
		}
		path := strings.TrimRight(loc.Path, "/")
		prefix := "/" + c.Repo + "/releases"
		if !strings.EqualFold(path, prefix) && !strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix)+"/") {
			return "", errUnexpected{"redirect to " + loc.Path + ", not a release of " + c.Repo}
		}
		if strings.EqualFold(path, prefix) {
			return "", nil // redirect to /releases: the repository has no release yet
		}
		m := tagRE.FindStringSubmatch(path)
		if m == nil {
			return "", errUnexpected{"redirect to " + loc.Path + ", which is not a release tag"}
		}
		return m[1], nil
	default:
		return "", httpStatusError(resp.StatusCode)
	}
}

type httpStatusError int

func (e httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", int(e)) }

// cause names a failure in words the administrator can act on, and keeps the raw reason as the detail.
func cause(err error) (short, detail string) {
	detail = err.Error()
	var (
		dns     *net.DNSError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		inval   x509.CertificateInvalidError
		tlsv    *tls.CertificateVerificationError
		status  httpStatusError
		unexp   errUnexpected
		netErr  net.Error
	)
	switch {
	case errors.As(err, &status):
		if status == http.StatusForbidden || status == http.StatusTooManyRequests {
			return fmt.Sprintf("HTTP %d (rate limit)", int(status)), detail
		}
		return fmt.Sprintf("HTTP %d", int(status)), detail
	case errors.As(err, &unexp):
		return "unexpected answer from GitHub", detail
	case errors.As(err, &tlsv), errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &inval):
		return "TLS error (certificate)", detail
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "Timeout", detail
	case errors.As(err, &dns):
		return "Connection error (no connection or DNS)", detail
	case errors.Is(err, context.Canceled):
		return "Cancelled", detail
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return "Connection error (no connection or DNS)", detail
	}
	return "Request failed", detail
}

// ParseVersion reads MAJOR[.MINOR[.PATCH...]] numerically, with an optional leading v and an optional -suffix or +build that is ignored.
// ok is false for anything else ("dev", "", "latest").
func ParseVersion(s string) (parts []int, ok bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return nil, false
	}
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// Newer reports whether latest is a higher version than current, comparing part by part and padding the shorter with zeros
// (1.10.0 is newer than 1.9.0, 2.1 equals 2.1.0). A string that does not parse is never newer; that is not an error.
func Newer(latest, current string) bool {
	l, ok1 := ParseVersion(latest)
	c, ok2 := ParseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < len(l) || i < len(c); i++ {
		var a, b int
		if i < len(l) {
			a = l[i]
		}
		if i < len(c) {
			b = c[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}
