package client

import (
	"strings"
	"unicode"
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

// TerminalSafe makes s safe to print to a terminal: control characters (C0
// but newline and tab, DEL, C1), bidi controls and invalid UTF-8 become
// U+FFFD, so what another account wrote is shown, never run or reordered.
// JSON output needs none of this: its encoder escapes them already.
func TerminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069,
			r == 0x200e, r == 0x200f, r == 0x061c:
			return utf8.RuneError
		}
		return r
	}, s)
}
