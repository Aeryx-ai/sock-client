package client

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ScrubSecrets is env without sock's credentials: SOCK_TOKEN,
// SOCK_ACCESS_GATE and CLAUDE_PLUGIN_OPTION_*, names matched without case
// (Windows ignores it). Everything else, GIT_* included, is kept.
func ScrubSecrets(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !isSecretName(kv) {
			out = append(out, kv)
		}
	}
	return out
}

func isSecretName(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	name = strings.ToUpper(name)
	return name == "SOCK_TOKEN" || name == "SOCK_ACCESS_GATE" || strings.HasPrefix(name, "CLAUDE_PLUGIN_OPTION_")
}

// TerminalSafe renders s so a terminal shows it rather than runs it: C0
// controls but newline and tab, DEL, C1 controls, bidi controls and bytes
// that are not UTF-8 come out as visible \xNN or \uNNNN escapes.
func TerminalSafe(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r >= 0x80 && r <= 0x9f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069,
			r == 0x200e, r == 0x200f, r == 0x061c:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}
