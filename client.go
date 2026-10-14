package client

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/guygrigsby/sock-contract/gen/go/sock/v1/sockv1connect"
)

// UnaryTimeout bounds each attempt of a unary call; Subscribe has no deadline.
const UnaryTimeout = 30 * time.Second

// AccessGateHeader carries the staging access gate.
const AccessGateHeader = "Sock-Access-Gate"

// Clients are the sock services a caller of the API uses.
type Clients struct {
	Identity   sockv1connect.IdentityServiceClient
	Messaging  sockv1connect.MessagingServiceClient
	Moderation sockv1connect.ModerationServiceClient
}

// New builds clients that send cfg.Token (when there is one: sign-in and
// registration run before a token exists) and cfg.AccessGate (when set) to the
// configured scheme and host only, and userAgent everywhere. New does not
// validate cfg; callers that need a token call Validate first. No client-wide
// timeout: Subscribe streams indefinitely. Redirects are never followed.
func New(cfg Config, userAgent string) Clients {
	base, _ := url.Parse(cfg.URL) // Validate checked the URL; a nil-safe zero URL matches nothing
	if base == nil {
		base = &url.URL{}
	}
	hc := &http.Client{
		Transport: &transport{
			base:   http.DefaultTransport,
			token:  cfg.Token,
			gate:   cfg.AccessGate,
			ua:     userAgent,
			scheme: base.Scheme,
			host:   base.Host,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return Clients{
		Identity:   sockv1connect.NewIdentityServiceClient(hc, cfg.URL),
		Messaging:  sockv1connect.NewMessagingServiceClient(hc, cfg.URL),
		Moderation: sockv1connect.NewModerationServiceClient(hc, cfg.URL),
	}
}

type transport struct {
	base         http.RoundTripper
	token, gate  string
	ua           string
	scheme, host string
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.URL.Scheme == t.scheme && r.URL.Host == t.host {
		if t.token != "" {
			r.Header.Set("Authorization", "Bearer "+t.token)
		}
		if t.gate != "" {
			r.Header.Set(AccessGateHeader, t.gate)
		}
	}
	r.Header.Set("User-Agent", t.ua)
	return t.base.RoundTrip(r)
}

// Call runs one unary RPC under r with a deadline per attempt. The error is
// already actionable text (see Describe).
func Call[T any](ctx context.Context, r Retrier, fn func(context.Context) (T, error)) (T, error) {
	var out T
	err := r.Do(ctx, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, UnaryTimeout)
		defer cancel()
		var err error
		out, err = fn(ctx)
		return err
	})
	return out, Describe(err)
}
