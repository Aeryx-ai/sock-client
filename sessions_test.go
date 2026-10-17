package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
)

func newSessions(t *testing.T, f *fakePuppets) (*SessionPuppets, *[]string) {
	t.Helper()
	var mu sync.Mutex
	renames := &[]string{}
	return &SessionPuppets{
		API:  f,
		Dir:  filepath.Join(t.TempDir(), "state", "puppets", "scope"),
		Host: "mac",
		Rename: func(_ context.Context, token, name string) error {
			mu.Lock()
			defer mu.Unlock()
			*renames = append(*renames, token+" "+name)
			return nil
		},
	}, renames
}

var codexS1 = Session{Platform: Codex, ID: "01a141bb-0155-7ec3-ad46-26342a0ae0aa"}

func TestSessionEnsureMintsOnceNamedForTheSession(t *testing.T) {
	f := newFake()
	p, _ := newSessions(t, f)
	tok, err := p.Ensure(t.Context(), codexS1, "Fix the parser")
	if err != nil || TokenKind(tok) != KindPuppet {
		t.Fatalf("%q %v", tok, err)
	}
	again, err := p.Ensure(t.Context(), codexS1, "Fix the parser")
	if err != nil || again != tok {
		t.Fatalf("second ensure %q %v", again, err)
	}
	if !slices.Equal(f.creates, []string{"Fix the parser on mac"}) {
		t.Fatalf("creates %v", f.creates)
	}
	if got := p.Cached(codexS1); got != tok {
		t.Fatalf("cached %q", got)
	}
	other := Session{Platform: ClaudeCode, ID: codexS1.ID}
	if p.Cached(other) != "" {
		t.Fatal("another platform's session with the same id shares the puppet")
	}
	fi, err := os.Stat(filepath.Join(p.Dir, "sessions"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("sessions dir %v %v", fi, err)
	}
}

func TestSessionNameTakenGetsTheSessionsHash(t *testing.T) {
	f := newFake()
	f.taken["Fix the parser on mac"] = true
	p, _ := newSessions(t, f)
	if _, err := p.Ensure(t.Context(), codexS1, "Fix the parser"); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 2 || !strings.HasPrefix(f.creates[1], "Fix the parser ") || !strings.HasSuffix(f.creates[1], " on mac") || f.creates[1] == f.creates[0] {
		t.Fatalf("creates %v", f.creates)
	}
}

func TestSessionNameIsCutTo64(t *testing.T) {
	f := newFake()
	p, _ := newSessions(t, f)
	if _, err := p.Ensure(t.Context(), codexS1, strings.Repeat("é", 100)); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(f.creates[0])); n != 64 || !strings.HasSuffix(f.creates[0], " on mac") {
		t.Fatalf("%d runes: %q", n, f.creates[0])
	}
}

func TestSessionRenameOnlyWhenTheNameChanged(t *testing.T) {
	f := newFake()
	p, renames := newSessions(t, f)
	tok, _ := p.Ensure(t.Context(), codexS1, "first")
	if err := p.SetName(t.Context(), codexS1, "first"); err != nil || len(*renames) != 0 {
		t.Fatalf("renamed to the same name: %v %v", *renames, err)
	}
	if err := p.SetName(t.Context(), codexS1, "second"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetName(t.Context(), codexS1, "second"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*renames, []string{tok + " second on mac"}) {
		t.Fatalf("renames %v", *renames)
	}
}

func TestSessionArchiveAndUnarchiveByPuppet(t *testing.T) {
	f := newFake()
	p, _ := newSessions(t, f)
	if err := p.Archive(t.Context(), codexS1); err != nil || len(f.archives) != 0 {
		t.Fatalf("archived a session with no puppet: %v %v", f.archives, err)
	}
	if _, err := p.Ensure(t.Context(), codexS1, "s"); err != nil {
		t.Fatal(err)
	}
	if err := p.Archive(t.Context(), codexS1); err != nil {
		t.Fatal(err)
	}
	if err := p.Unarchive(t.Context(), codexS1); err != nil {
		t.Fatal(err)
	}
	id := f.firstID()
	if !slices.Equal(f.archives, []string{"archive " + id, "unarchive " + id}) {
		t.Fatalf("archives %v", f.archives)
	}
}

func TestSessionsListsTheCachedOnes(t *testing.T) {
	f := newFake()
	p, _ := newSessions(t, f)
	claude := Session{Platform: ClaudeCode, ID: "91f7361a-db99-474f-a14c-ed5714a087a6"}
	for _, s := range []Session{codexS1, claude} {
		if _, err := p.Ensure(t.Context(), s, "n"); err != nil {
			t.Fatal(err)
		}
	}
	got := p.Sessions()
	if len(got) != 2 || !slices.Contains(got, codexS1) || !slices.Contains(got, claude) {
		t.Fatalf("sessions %v", got)
	}
}

func TestParseSession(t *testing.T) {
	if s, err := ParseSession("codex:01a1-x_y"); err != nil || s != (Session{Platform: Codex, ID: "01a1-x_y"}) {
		t.Fatalf("%v %v", s, err)
	}
	for _, bad := range []string{"", "codex", "codex:", "other:x", "codex:a/b", "codex:a b", "claude-code:" + strings.Repeat("x", 65)} {
		if _, err := ParseSession(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	f := newFake()
	p, _ := newSessions(t, f)
	if _, err := p.Ensure(t.Context(), Session{Platform: Codex, ID: "../x"}, "n"); err == nil || len(f.creates) != 0 {
		t.Fatal("a session id that is not one was used")
	}
}

// A cached token the hub no longer takes (evicted by the token limit) gets a
// new token for the same puppet; a puppet the owner erased gets a new puppet.
func TestSessionEnsureReplacesADeadToken(t *testing.T) {
	f := newFake()
	p, _ := newSessions(t, f)
	first, err := p.Ensure(t.Context(), codexS1, "s")
	if err != nil {
		t.Fatal(err)
	}
	id := f.firstID()
	p.Check = func(_ context.Context, tok string) error {
		if tok == first {
			return withReason(connect.NewError(connect.CodeUnauthenticated, errors.New("evicted")), sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID)
		}
		return nil
	}
	second, err := p.Ensure(t.Context(), codexS1, "s")
	if err != nil || second == first || !slices.Equal(f.mints, []string{id}) || len(f.creates) != 1 {
		t.Fatalf("%q %v: mints %v creates %v", second, err, f.mints, f.creates)
	}
	if p.Cached(codexS1) != second {
		t.Fatal("the new token was not cached")
	}

	// the owner erased the puppet: minting for it fails, so a new one is made
	f.mu.Lock()
	delete(f.puppets, id)
	f.failMint = withReason(connect.NewError(connect.CodeNotFound, errors.New("gone")), sockv1.ErrorReason_ERROR_REASON_NOT_FOUND)
	f.mu.Unlock()
	p.Check = func(context.Context, string) error {
		return withReason(connect.NewError(connect.CodeUnauthenticated, errors.New("gone")), sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID)
	}
	if _, err := p.Ensure(t.Context(), codexS1, "s"); err != nil || len(f.creates) != 2 {
		t.Fatalf("%v: creates %v", err, f.creates)
	}
}

// The archive state is kept beside the token: a call goes out only when it
// changes, across restarts too.
func TestSessionSetArchivedCallsOnlyOnAChange(t *testing.T) {
	f := newFake()
	p, _ := newSessions(t, f)
	if _, err := p.Ensure(t.Context(), codexS1, "s"); err != nil {
		t.Fatal(err)
	}
	id := f.firstID()
	for _, want := range []bool{true, true, false, false, true} {
		if err := p.SetArchived(t.Context(), codexS1, want); err != nil {
			t.Fatal(err)
		}
	}
	restarted := &SessionPuppets{API: f, Dir: p.Dir, Host: "mac"}
	if err := restarted.SetArchived(t.Context(), codexS1, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.archives, []string{"archive " + id, "unarchive " + id, "archive " + id}) {
		t.Fatalf("archives %v", f.archives)
	}
}

func TestSessionSetArchivedKeepsTheStateWhenTheCallFails(t *testing.T) {
	f := newFake()
	p, _ := newSessions(t, f)
	if _, err := p.Ensure(t.Context(), codexS1, "s"); err != nil {
		t.Fatal(err)
	}
	f.failArchive = connect.NewError(connect.CodeUnimplemented, errors.New("not yet"))
	if err := p.SetArchived(t.Context(), codexS1, true); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("err %v", err)
	}
	f.failArchive = nil
	if err := p.SetArchived(t.Context(), codexS1, true); err != nil || len(f.archives) != 1 {
		t.Fatalf("the failed archive was taken as done: %v %v", f.archives, err)
	}
}
