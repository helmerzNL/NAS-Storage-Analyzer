package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The request the server makes goes to an address an administrator typed, so it is held to a few rules (server-side request
// forgery is topic 80 of the conformance statement):
//
//   - http or https only (CheckURL), and a redirect is not followed: it is reported as a failure, so the address that is stored is
//     the address that is called;
//   - the connection is refused when the address resolves to a link-local one (169.254.0.0/16, fe80::/10: where cloud machines
//     keep their credentials), to the unspecified address or to multicast. The check is on the address that is dialled, after the
//     name was resolved, so a name that resolves there does not get around it. Private networks and this machine itself are
//     allowed: that is where Home Assistant and Gotify live;
//   - the answer is never shown: only its status code is kept, so the request cannot be used to read what is behind an address;
//   - one attempt takes at most ten seconds, and at most 64 KiB of an answer is read.
const (
	attemptTimeout = 10 * time.Second
	maxAnswer      = 64 << 10
	userAgent      = "NASSA (NAS Storage Analyzer)"
)

type sender struct {
	client *http.Client
	// backoff is the wait before each attempt of a scan message: three attempts, the first at once.
	backoff []time.Duration
	sleep   func(context.Context, time.Duration) error
}

// forbidden reports why an address may not be dialled, or "".
func forbidden(ip net.IP) string {
	switch {
	case ip == nil:
		return "not an IP address"
	case ip.IsUnspecified():
		return "the unspecified address"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "a link-local address"
	case ip.IsMulticast():
		return "a multicast address"
	}
	return ""
}

func guardDial(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if why := forbidden(net.ParseIP(host)); why != "" {
		return fmt.Errorf("refused: the address resolves to %s", why)
	}
	return nil
}

func newSender() *sender {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: guardDial}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: attemptTimeout,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true,
	}
	return &sender{
		client: &http.Client{Transport: tr, Timeout: attemptTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		backoff: []time.Duration{0, 2 * time.Second, 8 * time.Second},
		sleep: func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				return ctx.Err()
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}

// ntfyPriority maps the 1-10 priority of a message to ntfy's 1-5.
func ntfyPriority(p int) int {
	switch {
	case p >= 8:
		return 4
	case p >= 4:
		return 3
	default:
		return 2
	}
}

func (s *sender) request(ctx context.Context, address, format string, m Message) (*http.Request, error) {
	var body []byte
	var ctype string
	if format == FormatText {
		body, ctype = []byte(m.Message), "text/plain; charset=utf-8"
	} else {
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		body, ctype = b, "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("the stored address is not usable")
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", userAgent)
	if format == FormatText {
		// ntfy reads these; another receiver ignores them. A header value is one line.
		req.Header.Set("Title", strings.Join(strings.Fields(m.Title), " "))
		req.Header.Set("Priority", strconv.Itoa(ntfyPriority(m.Priority)))
		tag := "warning"
		if m.Event == KindFinished || m.Event == KindTest {
			tag = "white_check_mark"
		}
		req.Header.Set("Tags", tag)
	}
	return req, nil
}

// explain turns an error of the HTTP client into a sentence without the address: the client's own text quotes the whole URL,
// token included.
func explain(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("no answer within %s", attemptTimeout)
	}
	if errors.Is(err, context.Canceled) {
		return "stopped before there was an answer"
	}
	return err.Error()
}

// once makes one attempt. retry says whether trying again can help: no answer, a server error, or "too many requests".
func (s *sender) once(ctx context.Context, address, format string, m Message) (status int, problem string, retry bool) {
	actx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	req, err := s.request(actx, address, format, m)
	if err != nil {
		return 0, err.Error(), false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, explain(err), ctx.Err() == nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, "", false
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return resp.StatusCode, fmt.Sprintf("the receiver answered with a redirect (HTTP %d), which is not followed: enter the address it points to", resp.StatusCode), false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return resp.StatusCode, fmt.Sprintf("the receiver answered HTTP %d", resp.StatusCode), true
	default:
		return resp.StatusCode, fmt.Sprintf("the receiver refused the message (HTTP %d): check the address and its token", resp.StatusCode), false
	}
}

// deliver sends the message, waiting waits[i] before attempt i, until one succeeds or trying again cannot help.
func (s *sender) deliver(ctx context.Context, address, format string, m Message, waits []time.Duration) (ok bool, status int, problem string, attempts int) {
	for _, wait := range waits {
		if err := s.sleep(ctx, wait); err != nil {
			if problem == "" {
				problem = "stopped before the message was sent"
			}
			return false, status, problem, attempts
		}
		attempts++
		var retry bool
		status, problem, retry = s.once(ctx, address, format, m)
		if problem == "" {
			return true, status, "", attempts
		}
		if !retry {
			break
		}
	}
	return false, status, problem, attempts
}
