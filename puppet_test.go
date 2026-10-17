package client

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aeryx-ai/sock-contract/gen/go/sock/v1/sockv1connect"
)

// puppetEnv is a getenv over vars with a private state and home directory,
// and opts reading the config file body (no file when body is "").
func puppetEnv(t *testing.T, vars map[string]string, body string) PuppetOptions {
	t.Helper()
	m := map[string]string{"XDG_STATE_HOME": filepath.Join(t.TempDir(), "state"), "HOME": t.TempDir()}
	for k, v := range vars {
		m[k] = v
	}
	path := filepath.Join(t.TempDir(), "sock", "config.toml")
	if body != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return PuppetOptions{Getenv: func(k string) string { return m[k] }, ConfigPath: path, UserAgent: "test", NoProjectHint: "pass a project"}
}

// one source, never a mix: SOCK_TOKEN, else the file's machine_key, else its
// puppet_token; the human's token is never a puppet credential
func TestLoadPuppetConfigPrecedence(t *testing.T) {
	const all = "url = \"https://file.example\"\ntoken = \"sock_st_human_secret\"\nmachine_key = \"sock_mk_file_secret\"\npuppet_token = \"sock_pt_file_secret\"\n"
	for name, tc := range map[string]struct {
		env       map[string]string
		body      string
		tok, url  string
		noneFound bool
	}{
		"env machine key wins": {env: map[string]string{"SOCK_TOKEN": "sock_mk_env_secret"}, body: all, tok: "sock_mk_env_secret", url: DefaultURL},
		"env puppet token wins when the file has no machine key":   {env: map[string]string{"SOCK_TOKEN": "sock_pt_env_secret"}, body: "puppet_token = \"sock_pt_file_secret\"\n", tok: "sock_pt_env_secret", url: DefaultURL},
		"a stale env puppet token loses to the file's machine key": {env: map[string]string{"SOCK_TOKEN": "sock_pt_env_secret"}, body: all, tok: "sock_mk_file_secret", url: "https://file.example"},
		"an empty env token is unset":                              {env: map[string]string{"SOCK_TOKEN": ""}, body: all, tok: "sock_mk_file_secret", url: "https://file.example"},
		"a blank env token is unset":                               {env: map[string]string{"SOCK_TOKEN": " \t\n"}, body: all, tok: "sock_mk_file_secret", url: "https://file.example"},
		"machine key over puppet":                                  {body: all, tok: "sock_mk_file_secret", url: "https://file.example"},
		"puppet token":                                             {body: "token = \"sock_st_human_secret\"\npuppet_token = \"sock_pt_file_secret\"\n", tok: "sock_pt_file_secret", url: DefaultURL},
		"never the login token":                                    {body: "token = \"sock_st_human_secret\"\n", noneFound: true},
		"no file":                                                  {noneFound: true},
		"file token ignores env url":                               {env: map[string]string{"SOCK_URL": "https://env.example"}, body: "puppet_token = \"sock_pt_file_secret\"\n", tok: "sock_pt_file_secret", url: DefaultURL},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := LoadPuppetConfig(puppetEnv(t, tc.env, tc.body))
			if tc.noneFound {
				if !errors.Is(err, ErrNoPuppetCredential) || err.Error() != "no sock credential: run `sock login` once" {
					t.Fatalf("got %+v, %v", c, err)
				}
				return
			}
			if err != nil || c.Token != tc.tok || c.URL != tc.url {
				t.Fatalf("got %+v, %v", c, err)
			}
		})
	}
}

func TestLoadPuppetConfigNamesABadKeyNotItsValue(t *testing.T) {
	_, err := LoadPuppetConfig(puppetEnv(t, nil, "machine_key = \"sock_pt_wrongkind_secret\"\n"))
	if err == nil || !strings.Contains(err.Error(), "machine_key in ") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}

// a machine key hub: CreatePuppet answers as identityFake does
func machineKeyHub(t *testing.T) (*identityFake, string) {
	t.Helper()
	f := &identityFake{}
	mux := http.NewServeMux()
	mux.Handle(sockv1connect.NewIdentityServiceHandler(f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func gitInit(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	return dir
}

// a work directory in a repository gets the repository's puppet, minted
// with the machine key
func TestResolvePuppetMintsTheWorkDirsProjectPuppet(t *testing.T) {
	f, url := machineKeyHub(t)
	proj := gitInit(t, filepath.Join(t.TempDir(), "proj"))
	o := puppetEnv(t, nil, fmt.Sprintf("url = %q\nmachine_key = \"sock_mk_dev_secret\"\npuppet_token = \"sock_pt_shared_secret\"\n", url))
	o.WorkDir = proj
	c, err := ResolvePuppet(t.Context(), o)
	if err != nil || c.Token != "sock_pt_d_s" || c.URL != url {
		t.Fatalf("got %+v, %v", c, err)
	}
	if f.create == nil || !strings.HasPrefix(f.create.GetName(), "proj on ") || f.auth[0] != "Bearer sock_mk_dev_secret" {
		t.Fatalf("create %+v as %v", f.create, f.auth)
	}
}

// the hook passes the directory sock-mcp starts in: inside PLUGIN_ROOT both
// land on the file's puppet_token, and nothing is minted
func TestResolvePuppetInThePluginRootUsesThePuppetToken(t *testing.T) {
	f, url := machineKeyHub(t)
	root := gitInit(t, filepath.Join(t.TempDir(), "plugins", "sock"))
	o := puppetEnv(t, map[string]string{"PLUGIN_ROOT": root}, fmt.Sprintf("url = %q\nmachine_key = \"sock_mk_dev_secret\"\npuppet_token = \"sock_pt_shared_secret\"\n", url))
	o.WorkDir = root
	var logged []string
	o.Log = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	c, err := ResolvePuppet(t.Context(), o)
	if err != nil || c.Token != "sock_pt_shared_secret" || c.URL != url {
		t.Fatalf("got %+v, %v", c, err)
	}
	if f.create != nil {
		t.Fatalf("minted %+v", f.create)
	}
	if len(logged) != 1 || strings.Contains(logged[0], "sock_") {
		t.Fatalf("logged %q", logged)
	}
}

// CLAUDE_PROJECT_DIR names the project as it does for sock-mcp, whatever the
// work directory, and is not second-guessed
func TestResolvePuppetClaudeProjectDirWins(t *testing.T) {
	f, url := machineKeyHub(t)
	proj := filepath.Join(t.TempDir(), "claudeproj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	o := puppetEnv(t, map[string]string{"SOCK_TOKEN": "sock_mk_dev_secret", "SOCK_URL": url, "CLAUDE_PROJECT_DIR": proj}, "")
	o.WorkDir = t.TempDir()
	if c, err := ResolvePuppet(t.Context(), o); err != nil || c.Token != "sock_pt_d_s" {
		t.Fatalf("got %+v, %v", c, err)
	}
	if f.create == nil || !strings.HasPrefix(f.create.GetName(), "claudeproj on ") {
		t.Fatalf("create %+v", f.create)
	}
}

// a machine key from SOCK_TOKEN never falls back to the file; the caller's
// hint ends the error
func TestResolvePuppetEnvKeyWithNoProjectFails(t *testing.T) {
	f, url := machineKeyHub(t)
	o := puppetEnv(t, map[string]string{"SOCK_TOKEN": "sock_mk_dev_secret", "SOCK_URL": url}, "puppet_token = \"sock_pt_shared_secret\"\n")
	o.WorkDir = t.TempDir()
	_, err := ResolvePuppet(t.Context(), o)
	if err == nil || err.Error() != "no project directory: the working directory is not in a git repository; pass a project" {
		t.Fatalf("err = %v", err)
	}
	if f.create != nil {
		t.Fatalf("minted %+v", f.create)
	}
}

func TestResolvePuppetFileKeyWithNoProjectAndNoPuppetTokenFails(t *testing.T) {
	_, url := machineKeyHub(t)
	o := puppetEnv(t, nil, fmt.Sprintf("url = %q\nmachine_key = \"sock_mk_dev_secret\"\n", url))
	o.WorkDir = t.TempDir()
	_, err := ResolvePuppet(t.Context(), o)
	if err == nil || err.Error() != "no project directory: Codex plugin servers start in the plugin root; use the config.toml route for per-project puppets" {
		t.Fatalf("err = %v", err)
	}
}

// any credential but a machine key is used as it is
func TestResolvePuppetPassesAPuppetTokenThrough(t *testing.T) {
	o := puppetEnv(t, map[string]string{"SOCK_TOKEN": "sock_pt_env_secret"}, "")
	o.WorkDir = "/nonexistent"
	if c, err := ResolvePuppet(t.Context(), o); err != nil || c.Token != "sock_pt_env_secret" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

// the hook resolves on every send (sock-cli-7ut): the sweep runs at most once
// per sweepInterval per cache, whoever calls
func TestResolvePuppetSweepsAtMostOncePerInterval(t *testing.T) {
	f, url := machineKeyHub(t)
	root := t.TempDir()
	a, b := gitInit(t, filepath.Join(root, "a")), gitInit(t, filepath.Join(root, "b"))
	o := puppetEnv(t, nil, fmt.Sprintf("url = %q\nmachine_key = \"sock_mk_dev_secret\"\n", url))
	resolve := func(dir string) {
		t.Helper()
		o.WorkDir = dir
		if _, err := ResolvePuppet(t.Context(), o); err != nil {
			t.Fatal(err)
		}
	}
	resolve(a)
	resolve(b)
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	resolve(b)
	if len(f.erased) != 0 {
		t.Fatalf("swept within the interval: %v", f.erased)
	}
	dir := ProjectsDir(o.Getenv, Config{URL: url, Token: "sock_mk_dev_secret"})
	old := time.Now().Add(-sweepInterval - time.Minute)
	if err := os.Chtimes(filepath.Join(dir, ".swept"), old, old); err != nil {
		t.Fatal(err)
	}
	resolve(b)
	if len(f.erased) != 1 {
		t.Fatalf("no sweep after the interval: %v", f.erased)
	}
}

// TrustCache uses the cached token without asking the hub: the hook's hot
// path, which recovers from TOKEN_INVALID by resolving again without it
func TestResolvePuppetTrustCacheSkipsTheTokenCheck(t *testing.T) {
	f, url := machineKeyHub(t)
	proj := gitInit(t, filepath.Join(t.TempDir(), "proj"))
	o := puppetEnv(t, nil, fmt.Sprintf("url = %q\nmachine_key = \"sock_mk_dev_secret\"\n", url))
	o.WorkDir = proj
	for range 2 {
		if _, err := ResolvePuppet(t.Context(), o); err != nil {
			t.Fatal(err)
		}
	}
	if f.getMes != 1 {
		t.Fatalf("checked the cached token %d times, want once", f.getMes)
	}
	o.TrustCache = true
	if c, err := ResolvePuppet(t.Context(), o); err != nil || c.Token != "sock_pt_d_s" {
		t.Fatalf("got %+v, %v", c, err)
	}
	if f.getMes != 1 {
		t.Fatalf("checked a trusted cached token: %d GetMe", f.getMes)
	}
}

// The plugin passes ${user_config.sock_token}, which may still hold the old
// single-puppet token: the machine key wins, and one line says why.
func TestStaleEnvPuppetTokenIsSaidOutLoud(t *testing.T) {
	o := puppetEnv(t, map[string]string{"SOCK_TOKEN": "sock_pt_env_secret"}, "machine_key = \"sock_mk_file_secret\"\n")
	var lines []string
	o.Log = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	if c, err := LoadPuppetConfig(o); err != nil || c.Token != "sock_mk_file_secret" {
		t.Fatalf("%+v %v", c, err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "SOCK_TOKEN") || strings.Contains(lines[0], "secret") {
		t.Fatalf("log lines %q", lines)
	}
}
