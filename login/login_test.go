package login

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const hub = "https://hub.example"

var stateRE = regexp.MustCompile(`^([0-9]{4,5})\.[A-Za-z0-9_-]{43}$`)

// shown is what the person was shown: the login URL's state, nonce hash
// and the listener port the state names.
type shown struct {
	url   *url.URL
	state string
	nonce string
	port  int
}

func parseShown(t *testing.T, raw string) shown {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Errorf("login URL %q: %v", raw, err)
		return shown{}
	}
	q := u.Query()
	s := shown{url: u, state: q.Get("state"), nonce: q.Get("nonce")}
	m := stateRE.FindStringSubmatch(s.state)
	if m == nil {
		t.Errorf("state %q has the wrong shape", s.state)
		return s
	}
	s.port, _ = strconv.Atoi(m[1])
	return s
}

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       5 * time.Second,
}

// callback sends what the hub's relay would, with host as the Host header.
func callback(t *testing.T, port int, host string, q url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/callback?%s", port, q.Encode()), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Errorf("callback: %v", err)
		return nil
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res
}

func get(t *testing.T, port int, path string) *http.Response {
	t.Helper()
	res, err := noRedirect.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		t.Errorf("get %s: %v", path, err)
		return nil
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res
}

func good(s shown) url.Values {
	return url.Values{"state": {s.state}, "code": {"c.code-1"}, "id_token": {"h.p.s"}}
}

func host(s shown) string { return "127.0.0.1:" + strconv.Itoa(s.port) }

func run(t *testing.T, o Options) (Result, error) {
	t.Helper()
	o.Hub = hub
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return Run(ctx, o)
}

func TestHappyPath(t *testing.T) {
	var s shown
	var cb, done *http.Response
	res, err := run(t, Options{Show: func(raw string) error {
		s = parseShown(t, raw)
		cb = callback(t, s.port, host(s), good(s))
		done = get(t, s.port, "/done")
		return nil
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s.url.Scheme+"://"+s.url.Host+s.url.Path != hub+"/apple/cli-login" {
		t.Fatalf("login URL %v", s.url)
	}
	if res.Code != "c.code-1" || res.IDToken != "h.p.s" {
		t.Fatalf("result %+v", res)
	}
	sum := sha256.Sum256([]byte(res.Nonce))
	if hex.EncodeToString(sum[:]) != s.nonce || len(res.Nonce) != 43 {
		t.Fatalf("nonce %q does not hash to %q", res.Nonce, s.nonce)
	}
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/done" {
		t.Fatalf("callback answered %d %q", cb.StatusCode, cb.Header.Get("Location"))
	}
	for _, r := range []*http.Response{cb, done} {
		if r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("missing private headers: %v", r.Header)
		}
	}
	if done.StatusCode != http.StatusOK {
		t.Fatalf("/done answered %d", done.StatusCode)
	}
}

func TestHubPathPrefixKept(t *testing.T) {
	var s shown
	o := Options{Show: func(raw string) error {
		s = parseShown(t, raw)
		callback(t, s.port, host(s), good(s))
		return nil
	}}
	o.Hub = "https://hub.example/base/"
	o.Timeout = 10 * time.Second
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if s.url.Path != "/base/apple/cli-login" {
		t.Fatalf("path %q", s.url.Path)
	}
}

func TestStateMismatchIgnored(t *testing.T) {
	var bad *http.Response
	res, err := run(t, Options{Show: func(raw string) error {
		s := parseShown(t, raw)
		q := good(s)
		q.Set("state", strconv.Itoa(s.port)+"."+strings.Repeat("A", 43))
		q.Set("code", "attacker")
		bad = callback(t, s.port, host(s), q)
		callback(t, s.port, host(s), good(s))
		return nil
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if bad.StatusCode != http.StatusBadRequest || res.Code != "c.code-1" {
		t.Fatalf("mismatched state: %d, result %+v", bad.StatusCode, res)
	}
}

func TestWrongHostIgnored(t *testing.T) {
	var bad []int
	res, err := run(t, Options{Show: func(raw string) error {
		s := parseShown(t, raw)
		for _, h := range []string{"localhost:" + strconv.Itoa(s.port), "evil.example", "127.0.0.1"} {
			q := good(s)
			q.Set("code", "attacker")
			bad = append(bad, callback(t, s.port, h, q).StatusCode)
		}
		callback(t, s.port, host(s), good(s))
		return nil
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, code := range bad {
		if code != http.StatusBadRequest {
			t.Fatalf("wrong Host answered %d", code)
		}
	}
	if res.Code != "c.code-1" {
		t.Fatalf("result %+v", res)
	}
}

func TestSecondCallbackRefused(t *testing.T) {
	var second *http.Response
	res, err := run(t, Options{Show: func(raw string) error {
		s := parseShown(t, raw)
		callback(t, s.port, host(s), good(s))
		q := good(s)
		q.Set("code", "second")
		second = callback(t, s.port, host(s), q)
		get(t, s.port, "/done")
		return nil
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if second == nil || second.StatusCode != http.StatusBadRequest || res.Code != "c.code-1" {
		t.Fatalf("second callback not refused: %v, result %+v", second, res)
	}
}

func TestListenerClosesAfterDone(t *testing.T) {
	var port int
	_, err := run(t, Options{Show: func(raw string) error {
		s := parseShown(t, raw)
		port = s.port
		callback(t, s.port, host(s), good(s))
		get(t, s.port, "/done")
		return nil
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		_ = c.Close()
		t.Fatal("listener still accepting after the sign-in")
	}
}

func TestTimeout(t *testing.T) {
	start := time.Now()
	_, err := run(t, Options{Timeout: 100 * time.Millisecond, Show: func(string) error { return nil }})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v, want ErrTimeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not honored")
	}
}

func TestShowFailureAborts(t *testing.T) {
	boom := errors.New("boom")
	if _, err := run(t, Options{Show: func(string) error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestAppleError(t *testing.T) {
	cases := map[string]struct{ sent, want, never string }{
		"documented":   {"user_cancelled_authorize", "user_cancelled_authorize", ""},
		"undocumented": {"click_evil_link", "Apple returned an error", "click_evil_link"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := run(t, Options{Show: func(raw string) error {
				s := parseShown(t, raw)
				callback(t, s.port, host(s), url.Values{"state": {s.state}, "error": {c.sent}})
				return nil
			}})
			if err == nil || !strings.Contains(err.Error(), c.want) || (c.never != "" && strings.Contains(err.Error(), c.never)) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPortRange(t *testing.T) {
	for _, p := range []int{80, 1023, 65536, -1} {
		if _, err := run(t, Options{Port: p, Show: func(string) error { return nil }}); err == nil || !strings.Contains(err.Error(), "1024") {
			t.Fatalf("port %d: got %v", p, err)
		}
	}
}

func TestFixedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := ln.Addr().(*net.TCPAddr).Port
	_, err = run(t, Options{Port: busy, Show: func(string) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("port %d is in use", busy)) || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("busy port: got %v", err)
	}
	_ = ln.Close()
	var got int
	_, err = run(t, Options{Port: busy, Show: func(raw string) error {
		s := parseShown(t, raw)
		got = s.port
		callback(t, s.port, host(s), good(s))
		return nil
	}})
	if err != nil || got != busy {
		t.Fatalf("fixed port: %d, %v", got, err)
	}
}

func TestBindsLoopbackOnly(t *testing.T) {
	_, err := run(t, Options{Show: func(raw string) error {
		s := parseShown(t, raw)
		defer callback(t, s.port, host(s), good(s))
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return err
		}
		for _, a := range addrs {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if c, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(s.port)), 200*time.Millisecond); err == nil {
				_ = c.Close()
				t.Errorf("listener reachable on %s", ip)
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}
