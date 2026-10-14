package client

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// headersAt sends one request through New's transport for a config pointing at
// configured (the test server when empty) and returns what target received.
func headersAt(t *testing.T, cfg Config, target string) http.Header {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	t.Cleanup(srv.Close)
	if cfg.URL == "" {
		cfg.URL = srv.URL
	}
	if target == "" {
		target = srv.URL
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &transport{base: http.DefaultTransport, token: cfg.Token, gate: cfg.AccessGate, ua: "sock-test/1", scheme: u.Scheme, host: u.Host}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return got
}

func TestTransportSendsTokenAndGateToTheConfiguredHost(t *testing.T) {
	h := headersAt(t, Config{Token: "sock_st_d_s", AccessGate: "gate-1"}, "")
	if h.Get("Authorization") != "Bearer sock_st_d_s" || h.Get(AccessGateHeader) != "gate-1" || h.Get("User-Agent") != "sock-test/1" {
		t.Fatalf("headers %v", h)
	}
}

func TestTransportKeepsTokenAndGateFromOtherHosts(t *testing.T) {
	h := headersAt(t, Config{URL: "https://api.example", Token: "sock_st_d_s", AccessGate: "gate-1"}, "")
	if h.Get("Authorization") != "" || h.Get(AccessGateHeader) != "" {
		t.Fatalf("secrets sent to another host: %v", h)
	}
	if h.Get("User-Agent") != "sock-test/1" {
		t.Fatal("the user agent goes everywhere")
	}
}

// sign-in and registration run before a token exists
func TestTransportOmitsAnEmptyTokenAndGate(t *testing.T) {
	h := headersAt(t, Config{}, "")
	if _, ok := h["Authorization"]; ok {
		t.Fatalf("Authorization sent without a token: %v", h)
	}
	if _, ok := h[AccessGateHeader]; ok {
		t.Fatalf("gate sent without one configured: %v", h)
	}
}

func TestGateNotSentOnRedirect(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	t.Cleanup(other.Close)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(hub.Close)
	c := New(Config{URL: hub.URL, Token: "sock_st_d_s", AccessGate: "gate-1"}, "t")
	if _, err := c.Identity.GetMe(t.Context(), connect.NewRequest(&sockv1.GetMeRequest{})); err == nil {
		t.Fatal("redirected call succeeded")
	}
	if reached.Load() {
		t.Fatal("redirect followed: the other host received the request")
	}
}

// the three services every caller of the API uses
func TestNewBuildsAModerationClient(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := New(Config{URL: srv.URL, Token: token}, "t")
	if c.Identity == nil || c.Messaging == nil || c.Moderation == nil {
		t.Fatalf("clients %+v", c)
	}
}
