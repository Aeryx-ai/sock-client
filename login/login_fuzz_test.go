package login

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const fuzzState = "54321.abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"

// FuzzCallback sends one callback with an arbitrary query. It is accepted
// only with exactly one matching state and either an error or both code and
// id_token; an Apple error code is passed on only when documented.
func FuzzCallback(f *testing.F) {
	f.Add("state=" + fuzzState + "&code=c&id_token=t")
	f.Add("state=" + fuzzState + "&error=user_cancelled_authorize")
	f.Add("state=" + fuzzState + "&error=%1b%5b31mpwned")
	f.Add("state=" + fuzzState + "&state=" + fuzzState + "&code=c&id_token=t")
	f.Add("state=" + fuzzState + "&code=c")
	f.Add("state=wrong&code=c&id_token=t")
	f.Add("state=" + fuzzState + "&code=&id_token=t")
	f.Add("%zz&state=" + fuzzState)
	f.Add("")
	f.Fuzz(func(t *testing.T, rawQuery string) {
		l := &listener{host: "127.0.0.1:54321", state: fuzzState, result: make(chan outcome, 1), done: make(chan struct{})}
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:54321/callback", nil)
		r.URL.RawQuery = rawQuery
		w := httptest.NewRecorder()
		l.callback(w, r)

		q := r.URL.Query()
		states := q["state"]
		valid := len(states) == 1 && states[0] == fuzzState &&
			(q.Has("error") || (q.Get("code") != "" && q.Get("id_token") != ""))
		var got *outcome
		select {
		case o := <-l.result:
			got = &o
		default:
		}
		if valid != (got != nil) {
			t.Fatalf("query %q: accepted=%v, want %v (status %d)", rawQuery, got != nil, valid, w.Code)
		}
		if got == nil {
			if w.Code != http.StatusBadRequest {
				t.Fatalf("query %q: refused with status %d, want 400", rawQuery, w.Code)
			}
			return
		}
		if w.Code != http.StatusSeeOther {
			t.Fatalf("query %q: accepted with status %d", rawQuery, w.Code)
		}
		if q.Has("error") {
			var ae *AppleError
			if !errors.As(got.err, &ae) {
				t.Fatalf("query %q: error outcome %v", rawQuery, got.err)
			}
			if ae.Code != "" && (!appleErrors[ae.Code] || ae.Code != q.Get("error")) {
				t.Fatalf("query %q: Apple error code %q passed on", rawQuery, ae.Code)
			}
			return
		}
		if got.err != nil || got.code != q.Get("code") || got.idToken != q.Get("id_token") {
			t.Fatalf("query %q: outcome %+v", rawQuery, *got)
		}
	})
}

// FuzzLoginURL checks the login URL keeps the hub's scheme and host, ends in
// /apple/cli-login and carries exactly state and the nonce's hash.
func FuzzLoginURL(f *testing.F) {
	for _, s := range []string{"https://hub.example", "https://hub.example/", "https://hub.example/base/?x=1#frag", "http://127.0.0.1:8080", "hub.example", "", "https://[::1]:9/%zz", "https://h/a%2Fb/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, hub string) {
		l := &listener{state: fuzzState, nonce: "nonce-value"}
		got, err := l.loginURL(hub)
		h, herr := url.Parse(hub)
		if herr != nil || h.Host == "" {
			if err == nil {
				t.Fatalf("loginURL(%q) = %q, want an error", hub, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("loginURL(%q): %v", hub, err)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("loginURL(%q) = %q does not parse: %v", hub, got, err)
		}
		if u.Scheme != h.Scheme || u.Host != h.Host {
			t.Fatalf("loginURL(%q) = %q changed scheme or host", hub, got)
		}
		if !strings.HasSuffix(u.Path, "/apple/cli-login") || u.Fragment != "" {
			t.Fatalf("loginURL(%q) = %q, bad path or fragment", hub, got)
		}
		sum := sha256.Sum256([]byte("nonce-value"))
		q := u.Query()
		if len(q) != 2 || q.Get("state") != fuzzState || q.Get("nonce") != hex.EncodeToString(sum[:]) {
			t.Fatalf("loginURL(%q) = %q, query %v", hub, got, q)
		}
	})
}
