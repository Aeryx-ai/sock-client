package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// TokenKindName is what a bearer token is by its prefix (ADR 10).
type TokenKindName int

// The kinds of token: a human session, a puppet's, a machine key's.
const (
	KindUnknown TokenKindName = iota
	KindSession
	KindPuppet
	KindMachineKey
)

// TokenKind reads the kind off the prefix. The server checks that the prefix
// and the device's kind agree; this only lets a client choose a path.
func TokenKind(token string) TokenKindName {
	switch {
	case strings.HasPrefix(token, "sock_st_"):
		return KindSession
	case strings.HasPrefix(token, "sock_pt_"):
		return KindPuppet
	case strings.HasPrefix(token, "sock_mk_"):
		return KindMachineKey
	}
	return KindUnknown
}

// PuppetInfo is a puppet the machine key may see: one it created.
type PuppetInfo struct{ ID, Name string }

// PuppetAPI is what a machine key may call (ADR 10): the four RPCs of its
// caller class, reduced to what project puppets need.
type PuppetAPI interface {
	CreatePuppet(ctx context.Context, name, tokenLabel string) (id, token string, err error)
	ListPuppets(ctx context.Context) ([]PuppetInfo, error)
	MintPuppetToken(ctx context.Context, puppetID, label string) (token string, err error)
	ErasePuppet(ctx context.Context, puppetID string) error
}

// NewPuppetAPI wraps an Identity client authenticated with a machine key.
// Calls retry transient failures under r; errors are the Connect errors, so
// Reason works on them.
func NewPuppetAPI(c Clients, r Retrier) PuppetAPI { return &connectPuppets{c: c, r: r} }

type connectPuppets struct {
	c Clients
	r Retrier
}

func (a *connectPuppets) CreatePuppet(ctx context.Context, name, label string) (string, string, error) {
	res, err := Call(ctx, a.r, func(ctx context.Context) (*sockv1.CreatePuppetResponse, error) {
		out, err := a.c.Identity.CreatePuppet(ctx, connect.NewRequest(&sockv1.CreatePuppetRequest{Name: name, TokenLabel: label}))
		if err != nil {
			return nil, err
		}
		return out.Msg, nil
	})
	if err != nil {
		return "", "", err
	}
	return res.GetPuppet().GetId(), res.GetToken(), nil
}

func (a *connectPuppets) ListPuppets(ctx context.Context) ([]PuppetInfo, error) {
	res, err := Call(ctx, a.r, func(ctx context.Context) (*sockv1.ListPuppetsResponse, error) {
		out, err := a.c.Identity.ListPuppets(ctx, connect.NewRequest(&sockv1.ListPuppetsRequest{}))
		if err != nil {
			return nil, err
		}
		return out.Msg, nil
	})
	if err != nil {
		return nil, err
	}
	var out []PuppetInfo
	for _, p := range res.GetPuppets() {
		out = append(out, PuppetInfo{ID: p.GetPuppet().GetId(), Name: p.GetPuppet().GetDisplayName()})
	}
	return out, nil
}

func (a *connectPuppets) MintPuppetToken(ctx context.Context, id, label string) (string, error) {
	res, err := Call(ctx, a.r, func(ctx context.Context) (*sockv1.MintPuppetTokenResponse, error) {
		out, err := a.c.Identity.MintPuppetToken(ctx, connect.NewRequest(&sockv1.MintPuppetTokenRequest{PuppetId: id, Label: label}))
		if err != nil {
			return nil, err
		}
		return out.Msg, nil
	})
	if err != nil {
		return "", err
	}
	return res.GetToken(), nil
}

func (a *connectPuppets) ErasePuppet(ctx context.Context, id string) error {
	_, err := Call(ctx, a.r, func(ctx context.Context) (*sockv1.ErasePuppetResponse, error) {
		out, err := a.c.Identity.ErasePuppet(ctx, connect.NewRequest(&sockv1.ErasePuppetRequest{PuppetId: id}))
		if err != nil {
			return nil, err
		}
		return out.Msg, nil
	})
	return err
}

// Projects keeps one puppet per project for a machine key (ADR 10): minted on
// first use, cached privately, erased when the repository is gone. sock-mcp and
// the CLI command the hook calls share it, so the hook and the server always
// agree on which puppet a project is.
type Projects struct {
	API PuppetAPI
	// Dir is the cache: DefaultProjectsDir.
	Dir string
	// Host is the machine's short name, the " on <host>" of a puppet's name.
	Host string
	// Log receives one line per notable event; never a token. Nil: silent.
	Log func(format string, args ...any)
}

// DefaultProjectsDir is ${XDG_STATE_HOME:-~/.local/state}/sock-integrations/puppets.
func DefaultProjectsDir(getenv func(string) string) string {
	state := getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(getenv("HOME"), ".local", "state")
	}
	return filepath.Join(state, "sock-integrations", "puppets")
}

// ShortHost is the hostname up to its first dot.
func ShortHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "host"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

func (p *Projects) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

// Project is where a directory's puppet belongs: the repository's root (every
// worktree and subdirectory of one repository agree), or the directory itself
// outside git.
func Project(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return abs, nil
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return abs, nil
	}
	root := common
	if filepath.Base(common) == ".git" {
		root = filepath.Dir(common)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return root, nil
}

func keyOf(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(sum[:])[:32]
}

func shortHash(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(sum[:])[:6]
}

// cleanName keeps what renders: no control, format or private-use runes.
func cleanName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// puppetName is "<project> on <host>", the project cut so the whole is at most
// 64 characters.
func puppetName(project, host string) string {
	suffix := " on " + cleanName(host)
	project = cleanName(project)
	if project == "" {
		project = "project"
	}
	room := 64 - utf8.RuneCountInString(suffix)
	if room < 1 {
		room = 1
	}
	if rs := []rune(project); len(rs) > room {
		project = strings.TrimSpace(string(rs[:room]))
	}
	return project + suffix
}

const tokenLabel = "sock-integrations"

// Ensure returns the token of dir's project puppet: the cached one, or a new
// one minted with the machine key and cached. Concurrent callers (two sessions
// opening one repository, the hook and the server) mint once: the cache entry
// is held under a lock.
func (p *Projects) Ensure(ctx context.Context, dir string) (string, error) {
	repo, err := Project(dir)
	if err != nil {
		return "", fmt.Errorf("project of %s: %w", dir, err)
	}
	key := keyOf(repo)
	if err := p.prepare(); err != nil {
		return "", err
	}
	unlock, err := p.lock(key)
	if err != nil {
		return "", err
	}
	defer unlock()
	if tok := p.cached(key); tok != "" {
		return tok, nil
	}
	name := puppetName(filepath.Base(repo), p.Host)
	id, tok, err := p.API.CreatePuppet(ctx, name, tokenLabel)
	if Reason(err) == sockv1.ErrorReason_ERROR_REASON_PUPPET_NAME_TAKEN {
		id, tok, err = p.nameTaken(ctx, repo, name)
	}
	if err != nil {
		return "", fmt.Errorf("make the puppet for %s: %w", filepath.Base(repo), Describe(err))
	}
	if TokenKind(tok) != KindPuppet {
		return "", errors.New("the hub returned something that is not a puppet token")
	}
	if err := p.store(key, repo, id, tok); err != nil {
		return "", err
	}
	p.logf("puppet %q ready for %s", name, filepath.Base(repo))
	return tok, nil
}

// nameTaken: the name is either our own puppet (the cache or the response was
// lost; the hub lists only puppets this key made, so take it back with a new
// token) or someone else's (a hash of the path makes it ours).
func (p *Projects) nameTaken(ctx context.Context, repo, name string) (string, string, error) {
	list, err := p.API.ListPuppets(ctx)
	if err == nil {
		for _, pu := range list {
			if pu.Name == name {
				tok, err := p.API.MintPuppetToken(ctx, pu.ID, tokenLabel)
				return pu.ID, tok, err
			}
		}
	}
	return p.API.CreatePuppet(ctx, puppetName(filepath.Base(repo)+"-"+shortHash(repo), p.Host), tokenLabel)
}

// Sweep erases the puppets of repositories that no longer exist and forgets
// them. Best effort: a failure keeps the entry for the next start. A repository
// is gone only when its parent directory is there and it is not: an unmounted
// volume is not a deleted repository.
func (p *Projects) Sweep(ctx context.Context) {
	ents, err := os.ReadDir(p.Dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		key, ok := strings.CutSuffix(e.Name(), ".meta")
		if !ok {
			continue
		}
		repo, id := p.meta(key)
		if repo == "" || id == "" {
			continue
		}
		if _, err := os.Stat(repo); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := os.Stat(filepath.Dir(repo)); err != nil {
			continue
		}
		unlock, err := p.lock(key)
		if err != nil {
			continue
		}
		err = p.API.ErasePuppet(ctx, id)
		if err != nil && Reason(err) != sockv1.ErrorReason_ERROR_REASON_NOT_FOUND {
			p.logf("sweep: could not erase the puppet of a removed repository: %v", Describe(err))
			unlock()
			continue
		}
		p.forget(key)
		unlock()
		p.logf("sweep: erased the puppet of a removed repository")
	}
}

func (p *Projects) prepare() error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("puppet cache: %w", err)
	}
	return os.Chmod(p.Dir, 0o700)
}

func (p *Projects) lock(key string) (func(), error) {
	if err := p.prepare(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(p.Dir, key+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("puppet cache lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("puppet cache lock: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func (p *Projects) cached(key string) string {
	b, err := os.ReadFile(filepath.Join(p.Dir, key))
	if err != nil {
		return ""
	}
	tok := strings.TrimSpace(string(b))
	if TokenKind(tok) != KindPuppet || strings.ContainsFunc(tok, unicode.IsSpace) {
		return ""
	}
	return tok
}

func (p *Projects) meta(key string) (repo, id string) {
	b, err := os.ReadFile(filepath.Join(p.Dir, key+".meta"))
	if err != nil {
		return "", ""
	}
	repo, id, _ = strings.Cut(strings.TrimRight(string(b), "\n"), "\n")
	return repo, id
}

func (p *Projects) store(key, repo, id, tok string) error {
	if err := writeAtomic(filepath.Join(p.Dir, key+".meta"), []byte(repo+"\n"+id+"\n")); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(p.Dir, key), []byte(tok+"\n"))
}

func (p *Projects) forget(key string) {
	for _, n := range []string{key, key + ".meta"} {
		_ = os.Remove(filepath.Join(p.Dir, n))
	}
}

// writeAtomic writes data 0600 through a temporary file and a rename, so a
// crash never leaves a half-written token.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("puppet cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("puppet cache: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("puppet cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("puppet cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("puppet cache: %w", err)
	}
	return nil
}
