//go:build unix

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// security review 3: git is not handed the machine key, nor the plugin's
// options that may carry it
func TestProjectDoesNotHandGitTheMachineKey(t *testing.T) {
	bin := t.TempDir()
	dump := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nenv > '" + dump + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SOCK_TOKEN", "sock_mk_dev_secret")
	t.Setenv("CLAUDE_PLUGIN_OPTION_MACHINE_KEY", "sock_mk_dev_secret")
	t.Setenv("GIT_DIR", "/elsewhere")
	if _, err := Project(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.HasPrefix(line, "SOCK_TOKEN=") || strings.HasPrefix(line, "CLAUDE_PLUGIN_OPTION_") || strings.HasPrefix(line, "GIT_") || strings.Contains(line, "sock_mk_") {
			t.Fatalf("git saw %q", line)
		}
	}
}

// second review 3: names are matched without case (Windows ignores it), and
// the access gate is kept from git too
func TestProjectDoesNotHandGitTheKeyInAnyCaseNorTheGate(t *testing.T) {
	got := scrubGitEnv([]string{"sock_token=sock_mk_x", "Git_Dir=/x", "claude_plugin_option_key=k", "SOCK_ACCESS_GATE=g", "Sock_Access_Gate=g", "PATH=/bin", "SOCK_URL=https://a"})
	if strings.Join(got, ",") != "PATH=/bin,SOCK_URL=https://a" {
		t.Fatalf("kept %v", got)
	}
}
