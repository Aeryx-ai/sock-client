package login

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

var b64 = base64.RawURLEncoding

// get sends one request to the listener's path, with the right Host.
func get(t *testing.T, port int, path string, q url.Values) *http.Response {
	t.Helper()
	res, err := noRedirect.Get(fmt.Sprintf("http://127.0.0.1:%d%s?%s", port, path, q.Encode()))
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return nil
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res
}

func runGoogle(t *testing.T, o GoogleOptions) (GoogleResult, error) {
	t.Helper()
	o.Hub = hub
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return Google(ctx, o)
}

func TestGoogleHappyPath(t *testing.T) {
	var s shown
	var challenge string
	var cb *http.Response
	res, err := runGoogle(t, GoogleOptions{Show: func(raw string) error {
		s = parseShown(t, raw)
		challenge = s.url.Query().Get("code_challenge")
		cb = get(t, s.port, "/google", url.Values{"state": {s.state}, "code": {"01ABC-code"}})
		getDone(t, s.port)
		return nil
	}})
	if err != nil {
		t.Fatalf("Google: %v", err)
	}
	if s.url.Scheme+"://"+s.url.Host+s.url.Path != hub+"/workos/desktop-login" || len(s.url.Query()) != 2 {
		t.Fatalf("login URL %v", s.url)
	}
	sum := sha256.Sum256([]byte(res.Verifier))
	if res.Code != "01ABC-code" || len(res.Verifier) != 43 || b64.EncodeToString(sum[:]) != challenge {
		t.Fatalf("result %+v, challenge %q", res, challenge)
	}
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/done" {
		t.Fatalf("callback answered %d %q", cb.StatusCode, cb.Header.Get("Location"))
	}
}

func TestGoogleRefusesBadCallbacks(t *testing.T) {
	var codes []int
	res, err := runGoogle(t, GoogleOptions{Show: func(raw string) error {
		s := parseShown(t, raw)
		for _, q := range []url.Values{
			{"state": {s.state}},
			{"state": {s.state}, "code": {""}},
			{"state": {s.state}, "code": {"has space"}},
			{"state": {s.state}, "code": {strings.Repeat("a", 513)}},
			{"state": {strconv.Itoa(s.port) + "." + strings.Repeat("A", 43)}, "code": {"attacker"}},
		} {
			codes = append(codes, get(t, s.port, "/google", q).StatusCode)
		}
		// Apple's path is not this run's
		codes = append(codes, get(t, s.port, "/callback", url.Values{"state": {s.state}, "code": {"c"}, "id_token": {"a.b.c"}}).StatusCode)
		get(t, s.port, "/google", url.Values{"state": {s.state}, "code": {"good"}})
		return nil
	}})
	if err != nil || res.Code != "good" {
		t.Fatalf("Google: %+v, %v", res, err)
	}
	for i, c := range codes {
		if c != http.StatusBadRequest && c != http.StatusNotFound {
			t.Fatalf("bad callback %d answered %d", i, c)
		}
	}
}

func TestGoogleErrors(t *testing.T) {
	for sent, want := range map[string]error{"access_denied": ErrCancelled, "server_error": ErrFailed, "\x1b[31mpwned": ErrFailed} {
		_, err := runGoogle(t, GoogleOptions{Show: func(raw string) error {
			s := parseShown(t, raw)
			get(t, s.port, "/google", url.Values{"state": {s.state}, "error": {sent}})
			return nil
		}})
		if !errors.Is(err, want) || strings.Contains(err.Error(), "pwned") {
			t.Fatalf("error %q: got %v, want %v", sent, err, want)
		}
	}
}

// passkeyShown is the passkey page URL's fragment.
func passkeyShown(t *testing.T, raw string) (url.Values, int) {
	t.Helper()
	page, frag, ok := strings.Cut(raw, "#")
	if !ok || page != DefaultPasskeyPage {
		t.Fatalf("passkey URL %q", raw)
	}
	f, err := url.ParseQuery(frag)
	if err != nil {
		t.Fatalf("fragment %q: %v", frag, err)
	}
	m := stateRE.FindStringSubmatch(f.Get("state"))
	if m == nil || m[1] != f.Get("port") {
		t.Fatalf("state %q, port %q", f.Get("state"), f.Get("port"))
	}
	port, _ := strconv.Atoi(m[1])
	return f, port
}

func runPasskey(t *testing.T, o PasskeyOptions) (PasskeyResult, error) {
	t.Helper()
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return Passkey(ctx, o)
}

var (
	challenge = []byte(strings.Repeat("c", 32))
	credID    = []byte(strings.Repeat("i", 16))
	cdj       = []byte(`{"type":"webauthn.get"}`)
	authData  = []byte(strings.Repeat("a", 37))
	sig       = []byte("signature")
	uh        = []byte(strings.Repeat("u", 16))
	attObj    = []byte("attestation")
)

func assertion(f url.Values) url.Values {
	return url.Values{"state": {f.Get("state")}, "credential_id": {b64.EncodeToString(credID)},
		"client_data_json": {b64.EncodeToString(cdj)}, "authenticator_data": {b64.EncodeToString(authData)},
		"signature": {b64.EncodeToString(sig)}, "user_handle": {b64.EncodeToString(uh)}}
}

func TestPasskeySignIn(t *testing.T) {
	var frag url.Values
	res, err := runPasskey(t, PasskeyOptions{Challenge: challenge, Show: func(raw string) error {
		f, port := passkeyShown(t, raw)
		frag = f
		get(t, port, "/passkey", assertion(f))
		return nil
	}})
	if err != nil {
		t.Fatalf("Passkey: %v", err)
	}
	if frag.Get("op") != "get" || frag.Get("challenge") != b64.EncodeToString(challenge) || frag.Has("user_name") {
		t.Fatalf("fragment %v", frag)
	}
	if string(res.CredentialID) != string(credID) || string(res.ClientDataJSON) != string(cdj) ||
		string(res.AuthenticatorData) != string(authData) || string(res.Signature) != string(sig) ||
		string(res.UserHandle) != string(uh) || res.AttestationObject != nil {
		t.Fatalf("result %+v", res)
	}
}

func TestPasskeyCreate(t *testing.T) {
	var frag url.Values
	ex := [][]byte{[]byte(strings.Repeat("x", 16)), []byte(strings.Repeat("y", 20))}
	res, err := runPasskey(t, PasskeyOptions{Challenge: challenge, Create: &PasskeyCreation{UserHandle: uh, UserName: "@me", Exclude: ex},
		Show: func(raw string) error {
			f, port := passkeyShown(t, raw)
			frag = f
			// an assertion is not a creation
			if c := get(t, port, "/passkey", assertion(f)).StatusCode; c != http.StatusBadRequest {
				t.Errorf("assertion to a create run answered %d", c)
			}
			get(t, port, "/passkey", url.Values{"state": {f.Get("state")}, "credential_id": {b64.EncodeToString(credID)},
				"client_data_json": {b64.EncodeToString(cdj)}, "attestation_object": {b64.EncodeToString(attObj)}})
			return nil
		}})
	if err != nil {
		t.Fatalf("Passkey: %v", err)
	}
	want := b64.EncodeToString(ex[0]) + "," + b64.EncodeToString(ex[1])
	if frag.Get("op") != "create" || frag.Get("user_handle") != b64.EncodeToString(uh) || frag.Get("user_name") != "@me" || frag.Get("exclude") != want {
		t.Fatalf("fragment %v", frag)
	}
	if string(res.AttestationObject) != string(attObj) || string(res.CredentialID) != string(credID) || res.Signature != nil {
		t.Fatalf("result %+v", res)
	}
}

func TestPasskeyRefusesBadFields(t *testing.T) {
	_, err := runPasskey(t, PasskeyOptions{Challenge: challenge, Show: func(raw string) error {
		f, port := passkeyShown(t, raw)
		for name, change := range map[string]func(url.Values){
			"short credential": func(q url.Values) { q.Set("credential_id", b64.EncodeToString(credID[:15])) },
			"long credential":  func(q url.Values) { q.Set("credential_id", b64.EncodeToString(make([]byte, 1024))) },
			"padded base64":    func(q url.Values) { q.Set("signature", base64.URLEncoding.EncodeToString([]byte("sign"))) },
			"no signature":     func(q url.Values) { q.Del("signature") },
			"big client data":  func(q url.Values) { q.Set("client_data_json", b64.EncodeToString(make([]byte, 4097))) },
			"long user handle": func(q url.Values) { q.Set("user_handle", b64.EncodeToString(make([]byte, 65))) },
			"wrong state":      func(q url.Values) { q.Set("state", f.Get("port")+"."+strings.Repeat("A", 43)) },
		} {
			q := assertion(f)
			change(q)
			if c := get(t, port, "/passkey", q).StatusCode; c != http.StatusBadRequest {
				t.Errorf("%s answered %d", name, c)
			}
		}
		get(t, port, "/passkey", assertion(f))
		return nil
	}})
	if err != nil {
		t.Fatalf("Passkey: %v", err)
	}
}

func TestPasskeyErrors(t *testing.T) {
	for sent, want := range map[string]error{"cancelled": ErrCancelled, "unsupported": ErrUnsupported, "failed": ErrFailed, "other": ErrFailed} {
		_, err := runPasskey(t, PasskeyOptions{Challenge: challenge, Show: func(raw string) error {
			f, port := passkeyShown(t, raw)
			get(t, port, "/passkey", url.Values{"state": {f.Get("state")}, "error": {sent}})
			return nil
		}})
		if !errors.Is(err, want) {
			t.Fatalf("error %q: got %v, want %v", sent, err, want)
		}
	}
}

func TestPasskeyChallengeRequired(t *testing.T) {
	_, err := runPasskey(t, PasskeyOptions{Challenge: make([]byte, 31), Show: func(string) error {
		t.Fatal("shown without a valid challenge")
		return nil
	}})
	if err == nil {
		t.Fatal("31-byte challenge accepted")
	}
}
