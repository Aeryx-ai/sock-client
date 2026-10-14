package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PuppetOptions are the inputs to ResolvePuppet: everything sock-mcp and
// `sock send --as-puppet` differ in. The decision itself is ResolvePuppet's,
// so the server and the hook always agree on which puppet speaks.
type PuppetOptions struct {
	Getenv func(string) string
	// ConfigPath is the CLI's config file; "" is DefaultConfigPath(Getenv).
	ConfigPath string
	// WorkDir is the directory the caller stands in, checked for a project
	// as sock-mcp checks its working directory; "" is os.Getwd.
	// CLAUDE_PROJECT_DIR, when set, names the project instead, unchecked.
	WorkDir string
	// NoProjectHint ends the error for a SOCK_TOKEN machine key whose work
	// directory names no project: what the caller's user can do about it.
	NoProjectHint string
	// UserAgent is sent while making the project's puppet.
	UserAgent string
	// Log takes one line per notable event, never a secret. Nil: silent.
	Log func(format string, args ...any)
}

func (o PuppetOptions) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

// ErrNoPuppetCredential means no source holds a credential a puppet may use:
// SOCK_TOKEN is unset and the config file has neither machine_key nor
// puppet_token. The file's token, the human's session, never counts.
var ErrNoPuppetCredential = errors.New("no token: set SOCK_TOKEN")

// errNoProject is the failure when the work directory names no project and
// the config file has no puppet_token to fall back to.
var errNoProject = errors.New("no project directory: Codex plugin servers start in the plugin root; use the config.toml route for per-project puppets")

// ResolvePuppet is the configuration a puppet speaks with: LoadPuppetConfig,
// then ProjectPuppet. sock-mcp and `sock send --as-puppet` both call it.
func ResolvePuppet(ctx context.Context, o PuppetOptions) (Config, error) {
	cfg, err := LoadPuppetConfig(o)
	if err != nil {
		return Config{}, err
	}
	return ProjectPuppet(ctx, cfg, o)
}

// LoadPuppetConfig resolves the credential from one source, never a mix, so a
// token is only ever sent to the URL that came with it. With SOCK_TOKEN set it
// is SOCK_URL, SOCK_TOKEN and SOCK_ACCESS_GATE and the config file is not
// read. Otherwise it is the file's url and access_gate with its machine_key
// (`sock machine-key use` writes it), else its puppet_token (`sock puppets
// use` writes it); the human's token is never used, and neither found is
// ErrNoPuppetCredential. An ambient SOCK_URL or SOCK_ACCESS_GATE is then
// ignored, and Log says so once.
func LoadPuppetConfig(o PuppetOptions) (Config, error) {
	getenv := o.Getenv
	c := Config{URL: DefaultURL, Token: strings.TrimSpace(getenv("SOCK_TOKEN")), AccessGate: strings.TrimSpace(getenv("SOCK_ACCESS_GATE"))}
	if v := getenv("SOCK_URL"); v != "" {
		c.URL = v
	}
	if c.Token == "" {
		fc, path, err := o.fileConfig()
		if err != nil {
			return Config{}, err
		}
		tok, err := fileToken(fc, path)
		if err != nil {
			return Config{}, err
		}
		if tok == "" {
			return Config{}, ErrNoPuppetCredential
		}
		if getenv("SOCK_URL") != "" || getenv("SOCK_ACCESS_GATE") != "" {
			o.logf("ignoring SOCK_URL and SOCK_ACCESS_GATE: the token comes from %s, so the URL and gate do too", path)
		}
		c = fileServer(fc, tok)
	}
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// ProjectPuppet turns a machine key into the token of the project's puppet
// (ADR 10); any other credential is returned as it is. The project is
// CLAUDE_PROJECT_DIR, else WorkDir when it names one. When it does not
// (inside PLUGIN_ROOT, or outside any git repository) a key from the config
// file falls back to the file's puppet_token, and a key from SOCK_TOKEN
// fails: one source, never a mix. The machine key reaches the hub only to
// make, list and erase puppets. A failure never carries the key.
func ProjectPuppet(ctx context.Context, cfg Config, o PuppetOptions) (Config, error) {
	if TokenKind(cfg.Token) != KindMachineKey {
		return cfg, nil
	}
	getenv := o.Getenv
	dir := getenv("CLAUDE_PROJECT_DIR")
	if dir == "" {
		if dir = o.WorkDir; dir == "" {
			var err error
			if dir, err = os.Getwd(); err != nil {
				return cfg, fmt.Errorf("project puppet: %w", err)
			}
		}
		why, err := notAProject(ctx, dir, getenv("PLUGIN_ROOT"))
		if err != nil {
			return cfg, fmt.Errorf("project puppet: %w", err)
		}
		if why != "" && strings.TrimSpace(getenv("SOCK_TOKEN")) != "" {
			return cfg, fmt.Errorf("no project directory: %s; %s", why, o.NoProjectHint)
		}
		if why != "" {
			fb, path, ok, err := o.filePuppetConfig()
			if err != nil {
				return cfg, err
			}
			if !ok {
				return cfg, errNoProject
			}
			o.logf("%s, so no project puppet; using puppet_token from %s", why, path)
			return fb, nil
		}
	}
	projects := &Projects{
		API:  NewPuppetAPI(New(cfg, o.UserAgent), DefaultRetrier()),
		Dir:  ProjectsDir(getenv, cfg),
		Host: ShortHost(),
		Log:  o.Log,
	}
	// a cached token the hub rejects (the puppet was erased) is replaced
	projects.Check = NewTokenCheck(cfg, o.UserAgent)
	// separate budgets: a slow sweep must not leave Ensure without time
	sweepCtx, cancelSweep := context.WithTimeout(ctx, 20*time.Second)
	projects.Sweep(sweepCtx)
	cancelSweep()
	ensureCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tok, err := projects.Ensure(ensureCtx, dir)
	if err != nil {
		return cfg, errors.New(strings.ReplaceAll(err.Error(), cfg.Token, "[redacted]"))
	}
	cfg.Token = tok
	return cfg, nil
}

// notAProject says why dir names no project, or "" when it does. A Codex
// plugin server starts in the plugin cache with no project variable (a
// config.toml server starts in the project): a directory inside pluginRoot,
// or outside any git repository, would mint a puppet for the plugin rather
// than the project.
func notAProject(ctx context.Context, dir, pluginRoot string) (string, error) {
	if pluginRoot != "" && within(dir, pluginRoot) {
		return "the working directory is inside PLUGIN_ROOT", nil
	}
	in, err := InRepository(ctx, dir)
	if err != nil {
		return "", err
	}
	if !in {
		return "the working directory is not in a git repository", nil
	}
	return "", nil
}

// within reports whether dir is root or below it, symlinks resolved.
func within(dir, root string) bool {
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return p
	}
	rel, err := filepath.Rel(resolve(root), resolve(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// filePuppetConfig is the config file's puppet_token with the file's url and
// gate; ok is false when the file holds no puppet_token. path is the file,
// for a log line.
func (o PuppetOptions) filePuppetConfig() (c Config, path string, ok bool, err error) {
	fc, path, err := o.fileConfig()
	if err != nil {
		return Config{}, path, false, err
	}
	tok, err := puppetToken(fc, path)
	if err != nil || tok == "" {
		return Config{}, path, false, err
	}
	c = fileServer(fc, tok)
	if err := Validate(c); err != nil {
		return Config{}, path, false, err
	}
	return c, path, true, nil
}

// fileServer is tok with the file's url (else DefaultURL) and gate: a token
// from the file goes only where the file says.
func fileServer(fc FileConfig, tok string) Config {
	c := Config{URL: DefaultURL, Token: tok, AccessGate: strings.TrimSpace(fc.AccessGate)}
	if fc.URL != "" {
		c.URL = fc.URL
	}
	return c
}

// fileToken is the credential the config file gives a puppet: machine_key
// first, so each project gets its own puppet, then puppet_token. "" means
// neither is set. An error names the key, never its value.
func fileToken(fc FileConfig, path string) (string, error) {
	if mk := strings.TrimSpace(fc.MachineKey); mk != "" {
		if TokenKind(mk) != KindMachineKey {
			return "", fmt.Errorf("machine_key in %s is not a machine key (sock_mk_...); run sock machine-key use", path)
		}
		return mk, nil
	}
	return puppetToken(fc, path)
}

// puppetToken is the file's puppet_token, "" when unset.
func puppetToken(fc FileConfig, path string) (string, error) {
	pt := strings.TrimSpace(fc.PuppetToken)
	if pt != "" && TokenKind(pt) != KindPuppet {
		return "", fmt.Errorf("puppet_token in %s is not a puppet token (sock_pt_...); run sock puppets use <puppet id>", path)
	}
	return pt, nil
}

// fileConfig reads the config file. No home directory, or no file, is an
// empty config, not an error: the caller reports the missing credential.
func (o PuppetOptions) fileConfig() (FileConfig, string, error) {
	path := o.ConfigPath
	if path == "" {
		var err error
		if path, err = DefaultConfigPath(o.Getenv); err != nil {
			return FileConfig{}, "", nil
		}
	}
	fc, err := LoadFileConfig(path)
	return fc, path, err
}
