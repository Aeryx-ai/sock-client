package client

import (
	"strings"
	"testing"
)

func TestScrubSecretsDropsOnlyTheSecrets(t *testing.T) {
	got := ScrubSecrets([]string{"sock_token=sock_mk_x", "Git_Dir=/x", "claude_plugin_option_key=k", "SOCK_ACCESS_GATE=g", "Sock_Access_Gate=g", "PATH=/bin", "SOCK_URL=https://a", "GIT_SSH_COMMAND=ssh"})
	if strings.Join(got, ",") != "Git_Dir=/x,PATH=/bin,SOCK_URL=https://a,GIT_SSH_COMMAND=ssh" {
		t.Fatalf("kept %v", got)
	}
}

func TestTerminalSafeReplacesWhatATerminalWouldRun(t *testing.T) {
	const x = "\ufffd"
	for in, want := range map[string]string{
		"hi\x1b]52;c;cGF5bG9hZA==\x07there": "hi" + x + "]52;c;cGF5bG9hZA==" + x + "there", // OSC 52 clipboard write
		"a\x1b[2J\x1b[Hb":                   "a" + x + "[2J" + x + "[Hb",                   // CSI erase and home
		"x\u009b31my":                       "x" + x + "31my",                              // C1 CSI
		"raw\x9b2Jz":                        "raw" + x + "2Jz",                             // 8-bit CSI as a bare byte
		"del\x7f\r":                         "del" + x + x,
		"evil\u202etxt.exe\u2066\u200f\u061c\u202a\u2069\u200e": "evil" + x + "txt.exe" + x + x + x + x + x + x,
		"line one\n\tline two \u00fcn\u00ef":                    "line one\n\tline two \u00fcn\u00ef",
	} {
		if got := TerminalSafe(in); got != want {
			t.Errorf("TerminalSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
