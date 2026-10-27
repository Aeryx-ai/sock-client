// Package login runs the browser half of Sign in with Apple for a native sock
// client (sock login and sock register in the CLI, the desktop app)
// (sock-contract ADR 6): a one-shot loopback listener receives the Apple
// code and id_token the hub relays, bound to a state and a raw nonce this
// process alone holds.
package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is how long the listener waits for the browser.
const DefaultTimeout = 10 * time.Minute

// doneGrace is how long the listener stays up after the callback for the
// browser to load /done.
var doneGrace = 5 * time.Second

// ErrTimeout means no sign-in arrived before the timeout.
var ErrTimeout = errors.New("no sign-in arrived")

// Options configure one run.
type Options struct {
	// Hub is the server's base URL; the login page is <Hub>/apple/cli-login.
	Hub string
	// Port is the listener's port, 1024 to 65535; 0 picks any free one.
	Port int
	// Timeout bounds the wait; 0 means DefaultTimeout.
	Timeout time.Duration
	// Show presents the login URL to the person: opens a browser or prints
	// it. An error aborts the run.
	Show func(loginURL string) error
	// Received, when set, is called once when the callback is accepted, so
	// the person sees the sign-in arrived.
	Received func()
	// Claim, when set, derives the string whose SHA-256 goes to Apple from
	// the raw nonce, binding the id_token to one flow (operator sign-in,
	// sock-contract ADR 32). Nil sends the raw nonce's hash.
	Claim func(raw string) string
}

// Result is what the sign-in RPC needs.
type Result struct {
	Code    string
	IDToken string
	// Nonce is the raw nonce whose SHA-256 (or its Claim's) went to Apple.
	Nonce string
}

// AppleError is Apple refusing or the person cancelling.
type AppleError struct {
	// Code is Apple's error code when it is a documented one, else "".
	Code string
}

func (e *AppleError) Error() string {
	if e.Code == "" {
		return "Apple returned an error"
	}
	return "Apple returned " + e.Code
}

// appleErrors are the codes Apple's authorize endpoint documents, plus the
// OAuth 2.0 authorization errors (RFC 6749 4.1.2.1) it follows. Anything
// else is never printed: a local process could send any text.
var appleErrors = map[string]bool{
	"user_cancelled_authorize":  true,
	"invalid_request":           true,
	"invalid_client":            true,
	"invalid_grant":             true,
	"invalid_scope":             true,
	"unauthorized_client":       true,
	"unsupported_response_type": true,
	"access_denied":             true,
	"server_error":              true,
	"temporarily_unavailable":   true,
}

// Run binds the listener, shows the login URL, and waits for the first
// callback with the right Host and state.
func Run(ctx context.Context, o Options) (Result, error) {
	nonce := random()
	q, err := wait(ctx, flow{port: o.Port, timeout: o.Timeout, show: o.Show, received: o.Received},
		"/callback", appleCallback, func(state string) (string, error) {
			return appleLoginURL(o.Hub, state, nonce, o.Claim)
		})
	if err != nil {
		return Result{}, err
	}
	return Result{Code: q.Get("code"), IDToken: q.Get("id_token"), Nonce: nonce}, nil
}

// appleCallback takes Apple's relayed code and id_token, or its error.
func appleCallback(q url.Values) (bool, error) {
	if q.Has("error") {
		code := q.Get("error")
		if !appleErrors[code] {
			code = ""
		}
		return true, &AppleError{Code: code}
	}
	return q.Get("code") != "" && q.Get("id_token") != "", nil
}

// flow is what every browser handoff takes from its caller.
type flow struct {
	port     int
	timeout  time.Duration
	show     func(string) error
	received func()
}

// wait binds the listener, builds the URL to show from the state, shows it,
// and waits for the first request to path that carries the state and that
// accept takes. accept reports whether the query is well formed (else 400 and
// the wait goes on) and the flow's error, if the browser reported one.
func wait(ctx context.Context, f flow, path string, accept func(url.Values) (bool, error),
	build func(state string) (string, error)) (url.Values, error) {
	if f.port != 0 && (f.port < 1024 || f.port > 65535) {
		return nil, fmt.Errorf("--port must be 1024 to 65535, got %d", f.port)
	}
	timeout := f.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ln, err := listen(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port)))
	if err != nil && f.port != 0 {
		return nil, fmt.Errorf("port %d is in use or cannot be bound on 127.0.0.1; pick another --port (%w)", f.port, err)
	}
	if err != nil {
		return nil, fmt.Errorf("listen on 127.0.0.1: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if port < 1024 {
		_ = ln.Close()
		return nil, fmt.Errorf("listener got port %d; pass --port 1024 to 65535", port)
	}
	l := &listener{
		host:     "127.0.0.1:" + strconv.Itoa(port),
		state:    strconv.Itoa(port) + "." + random(),
		path:     path,
		accept:   accept,
		result:   make(chan outcome, 1),
		done:     make(chan struct{}),
		received: f.received,
	}
	shown, err := build(l.state)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	srv := &http.Server{Handler: l.routes(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx) // a browser that never loads /done must not hold sock open
		_ = srv.Close()
	}()

	if err := f.show(shown); err != nil {
		return nil, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var out outcome
	select {
	case out = <-l.result:
	case err := <-served:
		return nil, fmt.Errorf("listener stopped: %w", err)
	case <-timer.C:
		return nil, fmt.Errorf("%w within %s; run the command again", ErrTimeout, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Let the browser load /done so the result leaves its address bar.
	select {
	case <-l.done:
	case <-time.After(doneGrace):
	case <-ctx.Done():
	}
	return out.q, out.err
}

// random is 32 random bytes as 43 base64url characters.
func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(b)
}

type outcome struct {
	q   url.Values
	err error
}

type listener struct {
	host, state, path string
	accept            func(url.Values) (bool, error)
	result            chan outcome
	done              chan struct{}
	received          func()

	mu       sync.Mutex
	accepted bool
	doneOnce sync.Once
}

// hubURL is path on the hub with query q, keeping any path prefix the hub
// URL has and dropping its query and fragment.
func hubURL(hub, path string, q url.Values) (string, error) {
	u, err := url.Parse(hub)
	if err != nil || u.Host == "" {
		return "", errors.New("server URL is not a valid absolute URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func appleLoginURL(hub, state, nonce string, claim func(string) string) (string, error) {
	c := nonce
	if claim != nil {
		c = claim(nonce)
	}
	sum := sha256.Sum256([]byte(c))
	return hubURL(hub, "/apple/cli-login", url.Values{"state": {state}, "nonce": {hex.EncodeToString(sum[:])}})
}

const donePage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>sock login</title></head>
<body><p>sock received the sign-in. You can close this tab and return to the sock app or your terminal.</p></body>
</html>
`

func (l *listener) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+l.path, l.callback)
	mux.HandleFunc("GET /done", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		_, _ = w.Write([]byte(donePage))
		l.doneOnce.Do(func() { close(l.done) })
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Host != l.host {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// callback accepts exactly one request carrying this run's state that the
// run's accept takes. Anything else gets 400 and leaves the wait running.
func (l *listener) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	states := q["state"]
	if len(states) != 1 || subtle.ConstantTimeCompare([]byte(states[0]), []byte(l.state)) != 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ok, flowErr := l.accept(q)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	l.mu.Lock()
	first := !l.accepted
	l.accepted = true
	l.mu.Unlock()
	if !first {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if l.received != nil {
		l.received()
	}
	if flowErr != nil {
		q = nil
	}
	l.result <- outcome{q: q, err: flowErr}
	http.Redirect(w, r, "/done", http.StatusSeeOther)
}
