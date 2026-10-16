package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// Platform is the agent platform a session runs on.
type Platform string

// The platforms that have sessions.
const (
	ClaudeCode Platform = "claude-code"
	Codex      Platform = "codex"
)

// Session is one agent session: a Claude Code session or a Codex thread
// (sock-integrations docs/specs/session-puppets.md).
type Session struct {
	Platform Platform
	ID       string
}

var sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ParseSession reads "<platform>:<session id>".
func ParseSession(s string) (Session, error) {
	p, id, _ := strings.Cut(s, ":")
	out := Session{Platform: Platform(p), ID: id}
	return out, out.valid()
}

func (s Session) valid() error {
	if s.Platform != ClaudeCode && s.Platform != Codex {
		return fmt.Errorf("session platform must be %s or %s", ClaudeCode, Codex)
	}
	if !sessionIDRE.MatchString(s.ID) {
		return errors.New("session id must be 1 to 64 of A-Z a-z 0-9 _ -")
	}
	return nil
}

func (s Session) key() string {
	sum := sha256.Sum256([]byte(string(s.Platform) + ":" + s.ID))
	return hex.EncodeToString(sum[:])[:32]
}

// SessionPuppets keeps one puppet per agent session for a machine key
// (sock-contract ADR 27): minted the first time the session sends, cached
// privately, renamed with the session and archived while it is not running.
// Never erased. sock-mcp and the CLI share it, so the hooks and the server
// agree on a session's puppet.
type SessionPuppets struct {
	API PuppetAPI
	// Dir is the machine key's cache (ProjectsDir); sessions live under
	// Dir/sessions.
	Dir string
	// Host is the " on <host>" of a puppet's name.
	Host string
	// Rename sets a puppet's display name with its own token
	// (UpdateDisplayName, self).
	Rename func(ctx context.Context, token, name string) error
	Log    func(format string, args ...any)
}

func (p *SessionPuppets) cache() *Projects {
	return &Projects{Dir: filepath.Join(p.Dir, "sessions")}
}

// Ensure returns the session's puppet token: the cached one, or a new puppet
// named "<name> on <host>" made with the machine key. A name already taken gets
// a short hash of the session, so it is this session's alone.
func (p *SessionPuppets) Ensure(ctx context.Context, s Session, name string) (string, error) {
	if err := s.valid(); err != nil {
		return "", err
	}
	c := p.cache()
	unlock, err := c.lock(ctx, s.key())
	if err != nil {
		return "", err
	}
	defer unlock()
	if tok := c.cached(s.key()); tok != "" {
		return tok, nil
	}
	full := puppetName(name, p.Host)
	id, tok, err := p.API.CreatePuppet(ctx, full, tokenLabel)
	if Reason(err) == sockv1.ErrorReason_ERROR_REASON_PUPPET_NAME_TAKEN {
		full = puppetName(name+" "+s.key()[:6], p.Host)
		id, tok, err = p.API.CreatePuppet(ctx, full, tokenLabel)
	}
	if err != nil {
		return "", fmt.Errorf("make the puppet for this session: %w", Describe(err))
	}
	if TokenKind(tok) != KindPuppet {
		return "", errors.New("the hub returned something that is not a puppet token")
	}
	if err := p.store(s, id, full, tok); err != nil {
		return "", err
	}
	p.logf("puppet %q ready", full)
	return tok, nil
}

// Cached is the session's cached token, or "" without one. It never mints.
func (p *SessionPuppets) Cached(s Session) string {
	if s.valid() != nil {
		return ""
	}
	return p.cache().cached(s.key())
}

// SetName renames the session's puppet to "<name> on <host>" when that is not
// already its name. A session without a puppet is left alone.
func (p *SessionPuppets) SetName(ctx context.Context, s Session, name string) error {
	tok := p.Cached(s)
	if tok == "" || p.Rename == nil {
		return nil
	}
	id, old := p.meta(s)
	full := puppetName(name, p.Host)
	if full == old {
		return nil
	}
	if err := p.Rename(ctx, tok, full); err != nil {
		return fmt.Errorf("rename this session's puppet: %w", Describe(err))
	}
	return p.store(s, id, full, tok)
}

// Archive marks the session's puppet archived: its session is not running.
// Unarchive clears it. Both are idempotent at the hub; a session without a
// puppet is left alone.
func (p *SessionPuppets) Archive(ctx context.Context, s Session) error {
	if id, _ := p.meta(s); id != "" {
		return p.API.ArchivePuppet(ctx, id)
	}
	return nil
}

// Unarchive clears what Archive set: the session runs again.
func (p *SessionPuppets) Unarchive(ctx context.Context, s Session) error {
	if id, _ := p.meta(s); id != "" {
		return p.API.UnarchivePuppet(ctx, id)
	}
	return nil
}

// Sessions lists the sessions with a cached puppet.
func (p *SessionPuppets) Sessions() []Session {
	ents, err := os.ReadDir(p.cache().Dir)
	if err != nil {
		return nil
	}
	var out []Session
	for _, e := range ents {
		if key, ok := strings.CutSuffix(e.Name(), ".meta"); ok {
			if s, id, _ := p.metaOf(key); id != "" && s.key() == key {
				out = append(out, s)
			}
		}
	}
	return out
}

// meta is the session's puppet id and name, "" without an entry.
func (p *SessionPuppets) meta(s Session) (id, name string) {
	if s.valid() != nil {
		return "", ""
	}
	_, id, name = p.metaOf(s.key())
	return id, name
}

// metaOf reads an entry: "<platform>:<id>", the puppet id and the name, one per
// line.
func (p *SessionPuppets) metaOf(key string) (Session, string, string) {
	b, err := os.ReadFile(filepath.Join(p.cache().Dir, key+".meta"))
	if err != nil {
		return Session{}, "", ""
	}
	lines := strings.SplitN(strings.TrimRight(string(b), "\n"), "\n", 3)
	if len(lines) != 3 {
		return Session{}, "", ""
	}
	s, err := ParseSession(lines[0])
	if err != nil {
		return Session{}, "", ""
	}
	return s, lines[1], lines[2]
}

func (p *SessionPuppets) store(s Session, id, name, tok string) error {
	c := p.cache()
	if err := c.prepare(); err != nil {
		return err
	}
	meta := string(s.Platform) + ":" + s.ID + "\n" + id + "\n" + name + "\n"
	if err := writeAtomic(filepath.Join(c.Dir, s.key()+".meta"), []byte(meta)); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(c.Dir, s.key()), []byte(tok+"\n"))
}

func (p *SessionPuppets) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}
