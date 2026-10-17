package client

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/BurntSushi/toml"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

var tokenPrefixes = map[string]TokenKindName{
	"sock_st_": KindSession,
	"sock_pt_": KindPuppet,
	"sock_mk_": KindMachineKey,
}

func FuzzTokenKind(f *testing.F) {
	for _, s := range []string{"", "sock_st_abc", "sock_pt_dev_secret", "sock_mk_x", "sock_xx_y", "SOCK_PT_x", " sock_pt_x", "sock_pt", "sock_pt_ x\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := TokenKind(s)
		want := KindUnknown
		for p, k := range tokenPrefixes {
			if strings.HasPrefix(s, p) {
				want = k
			}
		}
		if got != want {
			t.Fatalf("TokenKind(%q) = %v, want %v", s, got, want)
		}
	})
}

func FuzzValidate(f *testing.F) {
	f.Add("https://api.joinsock.com", "sock_pt_abcdef123456", "")
	f.Add("http://127.0.0.1:8080", "sock_st_abcdef123456", "gate")
	f.Add("http://localhost/x", "sock_mk_abcdef123456", "")
	f.Add("http://evil.example/?k=supersecretquery", "sock_pt_abcdef123456", "")
	f.Add("https://user:hunter2hunter2@host", "sock_pt_abcdef123456", "")
	f.Add("user:hunter2hunter2@host", "sock_pt_abcdef123456", "")
	f.Add("https://host", "sock_pt_abc def", "")
	f.Add("https://host", "sock_pt_abc", "ga te\n")
	f.Add("ftp://host/x", "sock_xx_abcdef123456", "")
	f.Add("https://[::1", "sock_pt_abcdef123456", "")
	f.Fuzz(func(t *testing.T, rawURL, token, gate string) {
		err := Validate(Config{URL: rawURL, Token: token, AccessGate: gate})
		if err != nil {
			msg := err.Error()
			// Error text never echoes the token, nor any part of the URL
			// beyond scheme and host. Short values can match fixed text, so
			// only distinctive ones are checked.
			if len(token) >= 12 && !strings.Contains(rawURL, token) && strings.Contains(msg, token) {
				t.Fatalf("error echoes the token: %q", msg)
			}
			if u, perr := url.Parse(rawURL); perr == nil {
				if p, ok := u.User.Password(); ok && len(p) >= 8 && strings.Contains(msg, p) {
					t.Fatalf("error echoes the URL password: %q", msg)
				}
				if q := u.RawQuery; len(q) >= 8 && !strings.Contains(u.Host, q) && !strings.Contains(u.Scheme, q) && strings.Contains(msg, q) {
					t.Fatalf("error echoes the URL query: %q", msg)
				}
			}
			return
		}
		if strings.ContainsFunc(token, unicode.IsSpace) {
			t.Fatalf("accepted a token with whitespace: %q", token)
		}
		if TokenKind(token) == KindUnknown {
			t.Fatalf("accepted a token of unknown kind: %q", token)
		}
		if strings.ContainsFunc(gate, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			t.Fatalf("accepted an access gate with whitespace or a control character: %q", gate)
		}
		u, perr := url.Parse(rawURL)
		if perr != nil {
			t.Fatalf("accepted an unparsable URL %q: %v", rawURL, perr)
		}
		if u.User != nil || u.Host == "" || u.Opaque != "" {
			t.Fatalf("accepted URL %q with user, opaque part or no host", rawURL)
		}
		if u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname())) {
			t.Fatalf("accepted non-https URL %q off loopback", rawURL)
		}
	})
}

func FuzzRetryAfter(f *testing.F) {
	for _, s := range []string{"", "0", "1", "120", " 7 ", "-1", "+5", "1.5", "99999999999999999999", "9223372036854775807",
		"Wed, 21 Oct 2015 07:28:00 GMT", "Mon, 02 Jan 2006 15:04:05 GMT", "Thu, 01 Jan 1970 00:00:00 GMT", "garbage"} {
		f.Add(s, int64(1_700_000_000))
	}
	f.Fuzz(func(t *testing.T, header string, nowUnix int64) {
		now := time.Unix(nowUnix%(1<<40), 0).UTC()
		ce := connect.NewError(connect.CodeUnavailable, errors.New("x"))
		ce.Meta().Set("Retry-After", header)
		d, ok := RetryAfter(ce, now)
		if d < 0 {
			t.Fatalf("RetryAfter(%q) = %v, negative", header, d)
		}
		if !ok && d != 0 {
			t.Fatalf("RetryAfter(%q) = %v with ok false", header, d)
		}
		v := strings.TrimSpace(header)
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			want := time.Duration(min(n, maxRetryAfterSecs)) * time.Second
			if !ok || d != want {
				t.Fatalf("RetryAfter(%q) = %v, %v; want %v", header, d, ok, want)
			}
		}
		if at, err := time.Parse(http.TimeFormat, v); err == nil && !ok {
			t.Fatalf("RetryAfter(%q) refused a valid HTTP date %v", header, at)
		}
		// Not a Connect error: nothing to read.
		if d, ok := RetryAfter(errors.New(header), now); ok || d != 0 {
			t.Fatalf("RetryAfter(plain error) = %v, %v", d, ok)
		}
	})
}

// FuzzRetryAfterDateRoundTrip formats a future instant as an HTTP date and
// reads it back as the wait until then.
func FuzzRetryAfterDateRoundTrip(f *testing.F) {
	f.Add(int64(1_700_000_000), int64(0))
	f.Add(int64(1_700_000_000), int64(90))
	f.Add(int64(0), int64(-5))
	f.Fuzz(func(t *testing.T, nowUnix, delta int64) {
		now := time.Unix(nowUnix%(1<<34), 0).UTC()
		delta %= 1 << 31
		at := now.Add(time.Duration(delta) * time.Second)
		ce := connect.NewError(connect.CodeUnavailable, errors.New("x"))
		ce.Meta().Set("Retry-After", at.Format(http.TimeFormat))
		d, ok := RetryAfter(ce, now)
		if at.Year() < 0 || at.Year() > 9999 {
			return // outside what the date form can carry
		}
		want := time.Duration(max(delta, 0)) * time.Second
		if !ok || d != want {
			t.Fatalf("RetryAfter(%s) at %v = %v, %v; want %v", at.Format(http.TimeFormat), now, d, ok, want)
		}
	})
}

func FuzzDescribe(f *testing.F) {
	f.Add(uint32(connect.CodeResourceExhausted), int32(sockv1.ErrorReason_ERROR_REASON_RATE_LIMITED), "slow down", "30")
	f.Add(uint32(connect.CodeInvalidArgument), int32(sockv1.ErrorReason_ERROR_REASON_FIELD_INVALID), "text too long", "")
	f.Add(uint32(connect.CodeFailedPrecondition), int32(sockv1.ErrorReason_ERROR_REASON_LIMIT_REACHED), "", "")
	f.Add(uint32(connect.CodeUnavailable), int32(0), "", "")
	f.Add(uint32(0), int32(9999), "msg", "x")
	f.Add(uint32(99), int32(-1), "", "")
	f.Fuzz(func(t *testing.T, code uint32, reason int32, msg, retryAfter string) {
		var cause error
		if msg != "" {
			cause = errors.New(msg)
		}
		ce := connect.NewError(connect.Code(code), cause)
		if reason != 0 {
			ce = withReason(ce, sockv1.ErrorReason(reason))
		}
		if retryAfter != "" {
			ce.Meta().Set("Retry-After", retryAfter)
		}
		got := Describe(ce)
		if got == nil {
			t.Fatal("Describe(non-nil) = nil")
		}
		if got.Error() == "" {
			t.Fatal("Describe returned empty text")
		}
		var back *connect.Error
		if !errors.As(got, &back) || back != ce {
			t.Fatal("described error does not unwrap to the original")
		}
		if Reason(got) != Reason(ce) || Transient(got) != Transient(ce) {
			t.Fatal("Describe changed the classification")
		}
		if d, ok := RetryAfter(got, time.Now()); d < 0 || (!ok && d != 0) {
			t.Fatalf("RetryAfter(described) = %v, %v", d, ok)
		}
	})
}

// forbiddenInName are the runes cleanName drops besides control, format and
// private-use ones.
func forbiddenInName(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || r == utf8.RuneError ||
		(r >= 0xFE00 && r <= 0xFE0F) || r == 0x3164 || r == 0x2800 || r == 0x2028 || r == 0x2029
}

func FuzzCleanName(f *testing.F) {
	for _, s := range []string{"", "sock", "  my repo\t", "a\x1b[31mred", "rtl\u202eevil", "zw\u200bj", "\ufe0f", "\xff\xfe", "\u3164\u2800", "line\u2028sep", "\ue000private"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := cleanName(s)
		if !utf8.ValidString(got) {
			t.Fatalf("cleanName(%q) = %q, invalid UTF-8", s, got)
		}
		if strings.IndexFunc(got, forbiddenInName) >= 0 {
			t.Fatalf("cleanName(%q) = %q keeps a forbidden rune", s, got)
		}
		if strings.TrimSpace(got) != got {
			t.Fatalf("cleanName(%q) = %q has edge space", s, got)
		}
		if strings.ContainsFunc(got, func(r rune) bool { return unicode.IsSpace(r) && r != ' ' }) {
			t.Fatalf("cleanName(%q) = %q keeps a non-space whitespace", s, got)
		}
		if again := cleanName(got); again != got {
			t.Fatalf("cleanName not idempotent: %q then %q", got, again)
		}
	})
}

func FuzzPuppetName(f *testing.F) {
	f.Add("sock", "laptop")
	f.Add("", "")
	f.Add(strings.Repeat("long project name ", 10), "host")
	f.Add("p", strings.Repeat("h", 100))
	f.Add("\x1b[2J", "\u202ehost")
	f.Add("a"+strings.Repeat(" ", 70)+"b", "h")
	f.Fuzz(func(t *testing.T, project, host string) {
		got := puppetName(project, host)
		suffix := " on " + cleanName(host)
		if !strings.HasSuffix(got, suffix) {
			t.Fatalf("puppetName(%q, %q) = %q, want suffix %q", project, host, got, suffix)
		}
		head := strings.TrimSuffix(got, suffix)
		if head == "" || strings.TrimSpace(head) != head || strings.IndexFunc(head, forbiddenInName) >= 0 {
			t.Fatalf("puppetName(%q, %q) = %q, bad project part %q", project, host, got, head)
		}
		if n := utf8.RuneCountInString(suffix); n <= 63 && utf8.RuneCountInString(got) > 64 {
			t.Fatalf("puppetName(%q, %q) = %q, %d runes", project, host, got, utf8.RuneCountInString(got))
		}
	})
}

// FuzzLoadFileConfig fuzzes the parse; the file is always 0600, except a copy
// at 0644 that must be refused when it holds a secret, without echoing it.
func FuzzLoadFileConfig(f *testing.F) {
	f.Add(`url = "https://api.joinsock.com"`)
	f.Add("token = \"sock_st_abcdef\"\npuppet_token = \"sock_pt_x\"\nmachine_key = \"sock_mk_y\"\naccess_gate = \"g\"\n")
	f.Add("token = sock_st_unquotedsecret\n")
	f.Add("[table]\nkey = 1\n")
	f.Add("token = \"\"\"multi\nline\"\"\"")
	f.Add("unknown = 3\ntoken = \"sock_pt_z\"")
	f.Add("\xff\xfe")
	f.Add("token = 'sock_pt_literal'\ntoken = 'dup'")
	f.Fuzz(func(t *testing.T, data string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadFileConfig(path)
		var want FileConfig
		_, derr := toml.Decode(data, &want)
		if (err == nil) != (derr == nil) {
			t.Fatalf("LoadFileConfig err %v, toml.Decode err %v", err, derr)
		}
		if err != nil {
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("parse error does not name the file: %v", err)
			}
			return
		}
		if got != want {
			t.Fatalf("LoadFileConfig = %+v, want %+v", got, want)
		}

		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		open, err := LoadFileConfig(path)
		secrets := []string{want.Token, want.PuppetToken, want.MachineKey, want.AccessGate}
		holds := false
		for _, s := range secrets {
			holds = holds || s != ""
		}
		if !holds {
			if err != nil || open != want {
				t.Fatalf("0644 file without secrets: %+v, %v", open, err)
			}
			return
		}
		if err == nil || open != (FileConfig{}) {
			t.Fatalf("0644 file with a secret accepted: %+v", open)
		}
		for _, s := range secrets {
			if len(s) >= 8 && !strings.Contains(path, s) && strings.Contains(err.Error(), s) {
				t.Fatalf("mode refusal echoes a secret: %v", err)
			}
		}
	})
}
