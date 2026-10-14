package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFileConfigReadsTheKeys(t *testing.T) {
	p := writeConfig(t, 0o600, "url = \"https://x.example\"\ntoken = \"sock_pt_a\"\naccess_gate = \"g\"\n")
	f, err := LoadFileConfig(p)
	if err != nil || f.URL != "https://x.example" || f.Token != "sock_pt_a" || f.AccessGate != "g" {
		t.Fatalf("got %+v, %v", f, err)
	}
}

func TestLoadFileConfigMissingFileIsEmpty(t *testing.T) {
	for _, p := range []string{filepath.Join(t.TempDir(), "none.toml"), ""} {
		if f, err := LoadFileConfig(p); err != nil || f != (FileConfig{}) {
			t.Fatalf("%q: got %+v, %v", p, f, err)
		}
	}
}

func TestLoadFileConfigRefusesASecretOthersCanRead(t *testing.T) {
	for _, body := range []string{"token = \"sock_pt_a\"\n", "access_gate = \"g\"\n"} {
		for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
			p := writeConfig(t, mode, body)
			_, err := LoadFileConfig(p)
			if err == nil || !strings.Contains(err.Error(), "chmod 600") || !strings.Contains(err.Error(), p) {
				t.Fatalf("%q mode %o: want a refusal naming the file and chmod 600, got %v", body, mode, err)
			}
			if strings.Contains(err.Error(), "sock_pt_a") {
				t.Fatalf("the refusal echoes the token: %v", err)
			}
		}
	}
}

func TestLoadFileConfigReadableFileWithoutASecretIsFine(t *testing.T) {
	p := writeConfig(t, 0o644, "url = \"https://x.example\"\n")
	if f, err := LoadFileConfig(p); err != nil || f.URL != "https://x.example" {
		t.Fatalf("got %+v, %v", f, err)
	}
}

func TestLoadFileConfigParseErrorNamesTheFile(t *testing.T) {
	p := writeConfig(t, 0o600, "token = = nope\n")
	if _, err := LoadFileConfig(p); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if p, err := DefaultConfigPath(env(map[string]string{"XDG_CONFIG_HOME": "/xdg"})); err != nil || p != "/xdg/sock/config.toml" {
		t.Fatalf("got %q, %v", p, err)
	}
	if p, err := DefaultConfigPath(env(map[string]string{"HOME": "/home/u"})); err != nil || p != "/home/u/.config/sock/config.toml" {
		t.Fatalf("got %q, %v", p, err)
	}
	if _, err := DefaultConfigPath(env(nil)); err == nil {
		t.Fatal("no XDG_CONFIG_HOME and no HOME must be an error")
	}
}

// A malformed file must not echo what it holds: the parser quotes the text it
// choked on, and that text can be the token.
func TestLoadFileConfigParseErrorHidesContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("token = sock_pt_secretvalue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFileConfig(path)
	if err == nil {
		t.Fatal("malformed file parsed")
	}
	if strings.Contains(err.Error(), "secretvalue") {
		t.Fatalf("error leaks the file's contents: %v", err)
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("error does not say where: %v", err)
	}
}

func TestLoadFileConfigPuppetTokenIsASecretToo(t *testing.T) {
	p := writeConfig(t, 0o644, "puppet_token = \"sock_pt_a\"\n")
	if _, err := LoadFileConfig(p); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
	p = writeConfig(t, 0o600, "token = \"sock_st_h\"\npuppet_token = \"sock_pt_a\"\n")
	if f, err := LoadFileConfig(p); err != nil || f.PuppetToken != "sock_pt_a" || f.Token != "sock_st_h" {
		t.Fatalf("got %+v, %v", f, err)
	}
}

func TestLoadFileConfigMachineKeyIsASecretToo(t *testing.T) {
	p := writeConfig(t, 0o644, "machine_key = \"sock_mk_a\"\n")
	if _, err := LoadFileConfig(p); err == nil || !strings.Contains(err.Error(), "chmod 600") || strings.Contains(err.Error(), "sock_mk_a") {
		t.Fatalf("err = %v", err)
	}
	p = writeConfig(t, 0o600, "machine_key = \"sock_mk_a\"\n")
	if f, err := LoadFileConfig(p); err != nil || f.MachineKey != "sock_mk_a" {
		t.Fatalf("got %+v, %v", f, err)
	}
}
