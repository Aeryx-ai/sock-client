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

func TestTerminalSafeEscapesWhatATerminalWouldRun(t *testing.T) {
	for in, want := range map[string]string{
		"hi\x1b]52;c;cGF5bG9hZA==\x07there":   `hi\x1b]52;c;cGF5bG9hZA==\x07there`, // OSC 52 clipboard write
		"a\x1b[2J\x1b[Hb":                     `a\x1b[2J\x1b[Hb`,                   // CSI erase and home
		"x\u009b31my":                         `x\u009b31my`,                       // C1 CSI
		"raw\x9b2Jz":                          `raw\x9b2Jz`,                        // 8-bit CSI as a bare byte
		"del\x7f\r":                           `del\x7f\x0d`,
		"evil\u202etxt.exe\u2066\u200f\u061c": `evil\u202etxt.exe\u2066\u200f\u061c`,
		"line one\n\tline two ünï":            "line one\n\tline two ünï",
	} {
		if got := TerminalSafe(in); got != want {
			t.Errorf("TerminalSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
