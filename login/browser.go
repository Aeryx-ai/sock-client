package login

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"
)

// The browser's own outcomes for Google and passkeys (sock-contract
// google-and-passkey-sign-in-on-desktop). Nothing the browser sends is
// echoed: a local process could send any text.
var (
	ErrCancelled   = errors.New("sign-in cancelled")
	ErrFailed      = errors.New("the browser couldn't finish signing in")
	ErrUnsupported = errors.New("this browser can't use passkeys")
)

// GoogleOptions configure one Google run.
type GoogleOptions struct {
	// Hub is the server's base URL; the warning page is <Hub>/workos/desktop-login.
	Hub      string
	Port     int
	Timeout  time.Duration
	Show     func(loginURL string) error
	Received func()
}

// GoogleResult is what SignInWithGoogle, or AddEmailSignIn's google proof, needs.
type GoogleResult struct {
	Code string
	// Verifier is the PKCE verifier whose S256 challenge went to WorkOS.
	Verifier string
}

// Google runs Continue with Google through the hub's relay: the hub's
// warning page sends the browser to WorkOS, whose code the hub hands to this
// listener's /google.
func Google(ctx context.Context, o GoogleOptions) (GoogleResult, error) {
	verifier := random()
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q, err := wait(ctx, flow{port: o.Port, timeout: o.Timeout, show: o.Show, received: o.Received},
		"/google", googleCallback, func(state string) (string, error) {
			return hubURL(o.Hub, "/workos/desktop-login", url.Values{"state": {state}, "code_challenge": {challenge}})
		})
	if err != nil {
		return GoogleResult{}, err
	}
	return GoogleResult{Code: q.Get("code"), Verifier: verifier}, nil
}

func googleCallback(q url.Values) (bool, error) {
	if q.Has("error") {
		if q.Get("error") == "access_denied" {
			return true, ErrCancelled
		}
		return true, ErrFailed
	}
	code := q.Get("code")
	if len(code) < 1 || len(code) > 512 {
		return false, nil
	}
	for i := range len(code) {
		if code[i] < 0x21 || code[i] > 0x7e {
			return false, nil
		}
	}
	return true, nil
}

// DefaultPasskeyPage runs WebAuthn for relying party joinsock.com.
const DefaultPasskeyPage = "https://joinsock.com/passkey"

// PasskeyCreation is StartPasskeyRegistration's answer, for adding a passkey.
type PasskeyCreation struct {
	UserHandle []byte
	UserName   string
	Exclude    [][]byte
}

// PasskeyOptions configure one passkey run.
type PasskeyOptions struct {
	// Page is the passkey page; "" is DefaultPasskeyPage.
	Page string
	// Challenge is the hub's 32-byte challenge.
	Challenge []byte
	// Create, when set, adds a passkey; nil signs in.
	Create   *PasskeyCreation
	Port     int
	Timeout  time.Duration
	Show     func(pageURL string) error
	Received func()
}

// PasskeyResult is the platform's answer as bytes. A sign-in fills
// AuthenticatorData, Signature and UserHandle; a creation fills
// AttestationObject.
type PasskeyResult struct {
	CredentialID, ClientDataJSON             []byte
	AuthenticatorData, Signature, UserHandle []byte
	AttestationObject                        []byte
}

// Passkey runs one WebAuthn ceremony on the passkey page, which hands the
// result to this listener's /passkey. Its input travels in the URL fragment,
// which no server sees.
func Passkey(ctx context.Context, o PasskeyOptions) (PasskeyResult, error) {
	if len(o.Challenge) != 32 {
		return PasskeyResult{}, errors.New("passkey challenge must be 32 bytes")
	}
	page := o.Page
	if page == "" {
		page = DefaultPasskeyPage
	}
	enc := base64.RawURLEncoding.EncodeToString
	fields := []string{"credential_id", "client_data_json", "authenticator_data", "signature", "user_handle"}
	if o.Create != nil {
		fields = []string{"credential_id", "client_data_json", "attestation_object"}
	}
	q, err := wait(ctx, flow{port: o.Port, timeout: o.Timeout, show: o.Show, received: o.Received},
		"/passkey", func(q url.Values) (bool, error) { return passkeyCallback(q, fields) },
		func(state string) (string, error) {
			port, _, _ := strings.Cut(state, ".")
			f := url.Values{"op": {"get"}, "port": {port}, "state": {state}, "challenge": {enc(o.Challenge)}}
			if c := o.Create; c != nil {
				ex := make([]string, len(c.Exclude))
				for i, id := range c.Exclude {
					ex[i] = enc(id)
				}
				f.Set("op", "create")
				f.Set("user_handle", enc(c.UserHandle))
				f.Set("user_name", c.UserName)
				f.Set("exclude", strings.Join(ex, ","))
			}
			return page + "#" + f.Encode(), nil
		})
	if err != nil {
		return PasskeyResult{}, err
	}
	dec := func(k string) []byte {
		if !q.Has(k) {
			return nil
		}
		b, _ := base64.RawURLEncoding.DecodeString(q.Get(k)) // checked by passkeyCallback
		return b
	}
	return PasskeyResult{CredentialID: dec("credential_id"), ClientDataJSON: dec("client_data_json"),
		AuthenticatorData: dec("authenticator_data"), Signature: dec("signature"), UserHandle: dec("user_handle"),
		AttestationObject: dec("attestation_object")}, nil
}

// passkeySizes bounds each field in bytes, from the contract and WebAuthn.
var passkeySizes = map[string][2]int{
	"credential_id":      {16, 1023},
	"client_data_json":   {1, 4096},
	"authenticator_data": {37, 4096},
	"signature":          {1, 1024},
	"user_handle":        {1, 64},
	"attestation_object": {1, 16384},
}

func passkeyCallback(q url.Values, fields []string) (bool, error) {
	if q.Has("error") {
		switch q.Get("error") {
		case "cancelled":
			return true, ErrCancelled
		case "unsupported":
			return true, ErrUnsupported
		default:
			return true, ErrFailed
		}
	}
	want := map[string]bool{"state": true}
	for _, k := range fields {
		want[k] = true
		vs := q[k]
		if len(vs) != 1 {
			return false, nil
		}
		b, err := base64.RawURLEncoding.Strict().DecodeString(vs[0])
		size := passkeySizes[k]
		if err != nil || len(b) < size[0] || len(b) > size[1] {
			return false, nil
		}
	}
	for k := range q {
		if !want[k] {
			return false, nil
		}
	}
	return true, nil
}
