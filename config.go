// Package client is the sock API client shared by sock-cli and sock-mcp:
// config validation, an authenticated Connect transport, retries and error
// text.
package client

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

// DefaultURL is the public sock hub.
const DefaultURL = "https://api.joinsock.com"

// Config is what every client needs to reach the server.
type Config struct {
	URL   string
	Token string
	// AccessGate, when set, is sent as the Sock-Access-Gate header to the
	// configured server only (staging sits behind it).
	AccessGate string
}

// Validate checks c. Error text never echoes the raw URL or the token: both
// may carry credentials.
func Validate(c Config) error {
	if c.URL == "" {
		return errors.New("no server URL: set SOCK_URL")
	}
	if c.Token == "" {
		return errors.New("no token: set SOCK_TOKEN")
	}
	if strings.ContainsFunc(c.AccessGate, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("access gate contains whitespace or a control character; check for a stray line break")
	}
	if strings.ContainsFunc(c.Token, unicode.IsSpace) {
		return errors.New("token contains whitespace; check for a stray line break or a second value")
	}
	if TokenKind(c.Token) == KindUnknown {
		return errors.New("token must start with sock_pt_, sock_st_ or sock_mk_")
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // url.Error repeats the input; keep only the reason
		}
		return fmt.Errorf("server URL is not a valid URL: %w", err)
	}
	if u.User != nil {
		return errors.New("server URL must not contain a user or password; put the token in SOCK_TOKEN")
	}
	// A scheme-less "user:pass@host" parses as scheme "user" with the rest
	// in Opaque, so refuse Opaque and never print the URL here.
	if u.Host == "" || u.Opaque != "" {
		return errors.New("server URL is not a valid absolute https URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
	}
	// Only scheme and host: the path or query may carry secrets too.
	return fmt.Errorf("server URL %s://%s must use https (http only for loopback)", u.Scheme, u.Host)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
