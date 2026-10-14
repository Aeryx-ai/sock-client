package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
	"github.com/guygrigsby/sock-contract/gen/go/sock/v1/sockv1connect"
)

// fakePuppets is the machine key's view of the hub.
type fakePuppets struct {
	mu      sync.Mutex
	next    int
	puppets map[string]string // id -> name
	creates []string          // names asked for, in order
	mints   []string          // puppet ids a token was minted for
	erased  []string
	// failErase, when set, answers ErasePuppet with it; failList ListPuppets;
	// failCreate CreatePuppet
	failErase, failList, failCreate error
	// takenBy: names that exist and are not ours
	taken map[string]bool
}

func newFake() *fakePuppets {
	return &fakePuppets{puppets: map[string]string{}, taken: map[string]bool{}}
}

func (f *fakePuppets) CreatePuppet(_ context.Context, name, _ string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, name)
	if f.failCreate != nil {
		return "", "", f.failCreate
	}
	if f.taken[name] {
		return "", "", withReason(connect.NewError(connect.CodeFailedPrecondition, errors.New("taken")), sockv1.ErrorReason_ERROR_REASON_PUPPET_NAME_TAKEN)
	}
	for _, n := range f.puppets {
		if n == name {
			return "", "", withReason(connect.NewError(connect.CodeFailedPrecondition, errors.New("taken")), sockv1.ErrorReason_ERROR_REASON_PUPPET_NAME_TAKEN)
		}
	}
	f.next++
	id := "pup-" + string(rune('a'+f.next))
	f.puppets[id] = name
	return id, "sock_pt_" + id + "_secret", nil
}

func (f *fakePuppets) ListPuppets(context.Context) ([]PuppetInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList != nil {
		return nil, f.failList
	}
	var out []PuppetInfo
	for id, n := range f.puppets {
		out = append(out, PuppetInfo{ID: id, Name: n})
	}
	return out, nil
}

func (f *fakePuppets) MintPuppetToken(_ context.Context, id, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mints = append(f.mints, id)
	return "sock_pt_" + id + "_reminted", nil
}

func (f *fakePuppets) ErasePuppet(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErase != nil {
		return f.failErase
	}
	f.erased = append(f.erased, id)
	delete(f.puppets, id)
	return nil
}

func newProjects(t *testing.T, f *fakePuppets) *Projects {
	t.Helper()
	return &Projects{API: f, Dir: filepath.Join(t.TempDir(), "state", "puppets"), Host: "mac"}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %v: %v: %s", args, err, out)
	}
}

func TestEnsureMintsOnceAndCachesPrivately(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "myproj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tok, err := p.Ensure(t.Context(), dir)
	if err != nil || !strings.HasPrefix(tok, "sock_pt_") {
		t.Fatalf("token %q, %v", tok, err)
	}
	if len(f.creates) != 1 || f.creates[0] != "myproj on mac" {
		t.Fatalf("creates %v", f.creates)
	}
	again, err := p.Ensure(t.Context(), dir)
	if err != nil || again != tok || len(f.creates) != 1 {
		t.Fatalf("second ensure: %q %v, creates %v", again, err, f.creates)
	}
	di, err := os.Stat(p.Dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir %v, %v", di, err)
	}
	ents, _ := os.ReadDir(p.Dir)
	if len(ents) == 0 {
		t.Fatal("nothing cached")
	}
	for _, e := range ents {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", e.Name(), fi.Mode().Perm())
		}
	}
}

func TestEnsureWorktreesShareOneRepositoryPuppet(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "sockrepo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(root, "feature-branch")
	git(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
	f := newFake()
	p := newProjects(t, f)
	a, err := p.Ensure(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Ensure(t.Context(), wt)
	if err != nil || a != b || len(f.creates) != 1 || f.creates[0] != "sockrepo on mac" {
		t.Fatalf("worktree got %q (%v), creates %v", b, err, f.creates)
	}
	// a subdirectory of the repository is the same project
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if c, err := p.Ensure(t.Context(), sub); err != nil || c != a {
		t.Fatalf("subdir: %q %v", c, err)
	}
}

func TestEnsureNameTakenByAnotherPuppetGetsAPathHash(t *testing.T) {
	f := newFake()
	f.taken["proj on mac"] = true
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	if _, err := p.Ensure(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 2 || f.creates[0] != "proj on mac" || !strings.HasPrefix(f.creates[1], "proj-") || !strings.HasSuffix(f.creates[1], " on mac") || f.creates[1] == f.creates[0] {
		t.Fatalf("creates %v", f.creates)
	}
}

// A lost cache leaves the plain-named puppet behind: the name may be another
// repository's, so it is never taken back. The project gets a path-hashed puppet,
// which is reclaimable (TestHashedPuppetIsReclaimedAfterACacheLoss).
func TestEnsureAfterACacheLossDoesNotTakeThePlainNamedPuppetBack(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	first, err := p.Ensure(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(p.Dir)
	second, err := p.Ensure(t.Context(), dir)
	if err != nil || second == first || len(f.mints) != 0 || len(f.puppets) != 2 {
		t.Fatalf("second %q (%v), mints %v, puppets %v", second, err, f.mints, f.puppets)
	}
	if again, _ := p.Ensure(t.Context(), dir); again != second {
		t.Fatalf("not cached: %q", again)
	}
}

func TestEnsureLongProjectNamesFitTheLimit(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	p.Host = "a-rather-long-host"
	dir := filepath.Join(t.TempDir(), strings.Repeat("é", 100))
	_ = os.Mkdir(dir, 0o755)
	if _, err := p.Ensure(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	name := f.creates[0]
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 || !strings.HasSuffix(name, " on a-rather-long-host") || !utf8.ValidString(name) {
		t.Fatalf("name %q is %d runes", name, n)
	}
}

func TestEnsureNamesHaveNoControlCharacters(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "evil\x1b[31m\u202eproj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skip("filesystem refuses the name")
	}
	if _, err := p.Ensure(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.creates[0] {
		if r < 0x20 || r == 0x7f || r == 0x202e {
			t.Fatalf("name %q holds %U", f.creates[0], r)
		}
	}
}

func TestEnsureConcurrentCallsMintOnce(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	var wg sync.WaitGroup
	var bad atomic.Int32
	toks := make([]string, 8)
	for i := range toks {
		wg.Go(func() {
			tok, err := p.Ensure(context.Background(), dir)
			if err != nil {
				bad.Add(1)
			}
			toks[i] = tok
		})
	}
	wg.Wait()
	if bad.Load() != 0 || len(f.creates) != 1 {
		t.Fatalf("errors %d, creates %v", bad.Load(), f.creates)
	}
	for _, tk := range toks {
		if tk != toks[0] {
			t.Fatalf("tokens differ: %v", toks)
		}
	}
}

func TestEnsureIgnoresACorruptCacheEntry(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	if _, err := p.Ensure(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(p.Dir)
	for _, e := range ents {
		if !strings.Contains(e.Name(), ".") { // the token file
			_ = os.WriteFile(filepath.Join(p.Dir, e.Name()), []byte("garbage"), 0o600)
		}
	}
	tok, err := p.Ensure(t.Context(), dir)
	if err != nil || !strings.HasPrefix(tok, "sock_pt_") {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestSweepErasesPuppetsOfVanishedRepositories(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	root := t.TempDir()
	gone, kept := filepath.Join(root, "gone"), filepath.Join(root, "kept")
	_ = os.Mkdir(gone, 0o755)
	_ = os.Mkdir(kept, 0o755)
	if _, err := p.Ensure(t.Context(), gone); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Ensure(t.Context(), kept); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	p.Sweep(t.Context())
	if len(f.erased) != 1 || f.puppets["pup-c"] == "" || f.puppets["pup-b"] != "" || f.erased[0] != "pup-b" {
		t.Fatalf("erased %v, puppets %v", f.erased, f.puppets)
	}
	// the survivor is still cached; the swept one is not
	if _, err := p.Ensure(t.Context(), kept); err != nil || len(f.creates) != 2 {
		t.Fatalf("kept repo re-minted: %v creates %v", err, f.creates)
	}
}

// an unmounted volume is not a deleted repository
func TestSweepKeepsPuppetsWhenTheParentDirectoryIsGone(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	vol := t.TempDir()
	repo := filepath.Join(vol, "mount", "proj")
	_ = os.MkdirAll(repo, 0o755)
	if _, err := p.Ensure(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(vol, "mount"))
	p.Sweep(t.Context())
	if len(f.erased) != 0 {
		t.Fatalf("erased %v", f.erased)
	}
}

func TestSweepKeepsTheEntryWhenEraseFailsAndDropsItWhenTheHubForgotIt(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	root := t.TempDir()
	a := filepath.Join(root, "a")
	_ = os.Mkdir(a, 0o755)
	_ = os.WriteFile(filepath.Join(root, "keep"), nil, 0o600) // the parent is not empty
	if _, err := p.Ensure(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(a)
	f.failErase = Unavailable()
	p.Sweep(t.Context())
	ents, _ := os.ReadDir(p.Dir)
	if len(ents) == 0 {
		t.Fatal("entry dropped after a transient failure")
	}
	f.failErase = withReason(connect.NewError(connect.CodeNotFound, errors.New("no")), sockv1.ErrorReason_ERROR_REASON_NOT_FOUND)
	p.Sweep(t.Context())
	ents, _ = os.ReadDir(p.Dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".meta") || !strings.Contains(e.Name(), ".") {
			t.Fatalf("%s survives a NOT_FOUND erase", e.Name())
		}
	}
}

func TestProjectsBase(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := projectsBase(env(map[string]string{"XDG_STATE_HOME": "/x"})); got != "/x/sock-integrations/puppets" {
		t.Fatal(got)
	}
	if got := projectsBase(env(map[string]string{"HOME": "/h"})); got != "/h/.local/state/sock-integrations/puppets" {
		t.Fatal(got)
	}
}

func TestValidateAcceptsAMachineKey(t *testing.T) {
	c := Config{URL: DefaultURL, Token: "sock_mk_dev_secret"}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	if TokenKind("sock_mk_x_y") != KindMachineKey || TokenKind("sock_pt_x_y") != KindPuppet || TokenKind("sock_st_x_y") != KindSession || TokenKind("zzz") != KindUnknown {
		t.Fatal("TokenKind")
	}
}

type identityFake struct {
	sockv1connect.UnimplementedIdentityServiceHandler
	auth   []string
	create *sockv1.CreatePuppetRequest
	erased []string
	getMes int
}

func (f *identityFake) GetMe(_ context.Context, r *connect.Request[sockv1.GetMeRequest]) (*connect.Response[sockv1.GetMeResponse], error) {
	f.auth = append(f.auth, r.Header().Get("Authorization"))
	f.getMes++
	return connect.NewResponse(&sockv1.GetMeResponse{}), nil
}

func (f *identityFake) CreatePuppet(_ context.Context, r *connect.Request[sockv1.CreatePuppetRequest]) (*connect.Response[sockv1.CreatePuppetResponse], error) {
	f.auth = append(f.auth, r.Header().Get("Authorization"))
	f.create = r.Msg
	return connect.NewResponse(&sockv1.CreatePuppetResponse{Puppet: &sockv1.Account{Id: "p1"}, Token: "sock_pt_d_s"}), nil
}

func (f *identityFake) ErasePuppet(_ context.Context, r *connect.Request[sockv1.ErasePuppetRequest]) (*connect.Response[sockv1.ErasePuppetResponse], error) {
	f.auth = append(f.auth, r.Header().Get("Authorization"))
	f.erased = append(f.erased, r.Msg.GetPuppetId())
	return connect.NewResponse(&sockv1.ErasePuppetResponse{}), nil
}

// the adapter speaks to the hub as the machine key, and only that
func TestPuppetAPISendsTheMachineKey(t *testing.T) {
	f := &identityFake{}
	mux := http.NewServeMux()
	mux.Handle(sockv1connect.NewIdentityServiceHandler(f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL, Token: "sock_mk_dev_secret"}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	api := NewPuppetAPI(New(cfg, "test"), DefaultRetrier())
	id, tok, err := api.CreatePuppet(t.Context(), "proj on mac", "lbl")
	if err != nil || id != "p1" || tok != "sock_pt_d_s" || f.create.GetName() != "proj on mac" || f.create.GetTokenLabel() != "lbl" {
		t.Fatalf("%q %q %v %+v", id, tok, err, f.create)
	}
	if err := api.ErasePuppet(t.Context(), "p1"); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.auth {
		if a != "Bearer sock_mk_dev_secret" {
			t.Fatalf("authorization %q", a)
		}
	}
}

func TestTokenFileIsTheCachedTokensPath(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	tok, err := p.Ensure(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	path, err := p.TokenFile(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("%q %v", b, err)
	}
}

// review F1: two repositories called "api" are two projects
func TestSameBasenameRepositoriesGetTheirOwnPuppets(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "orgA", "api"), filepath.Join(root, "orgB", "api")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := newFake()
	p := newProjects(t, f)
	ta, err := p.Ensure(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := p.Ensure(t.Context(), b)
	if err != nil || ta == tb || len(f.mints) != 0 || len(f.puppets) != 2 {
		t.Fatalf("a %q b %q (%v) mints %v puppets %v", ta, tb, err, f.mints, f.puppets)
	}
	// removing a's repository erases a's puppet and not b's
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "orgA", "keep"), nil, 0o600) // the parent is not empty
	p.Sweep(t.Context())
	if len(f.erased) != 1 || len(f.puppets) != 1 {
		t.Fatalf("erased %v, puppets %v", f.erased, f.puppets)
	}
	if again, err := p.Ensure(t.Context(), b); err != nil || again != tb {
		t.Fatalf("b lost its puppet: %q %v", again, err)
	}
}

// review F2: a path-hashed puppet is found again after a cache loss
func TestHashedPuppetIsReclaimedAfterACacheLoss(t *testing.T) {
	f := newFake()
	f.taken["proj on mac"] = true
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	first, err := p.Ensure(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(p.Dir)
	second, err := p.Ensure(t.Context(), dir)
	if err != nil || second == first || len(f.mints) != 1 || len(f.puppets) != 1 {
		t.Fatalf("%q %v mints %v puppets %v", second, err, f.mints, f.puppets)
	}
}

// review F3: a cached token the hub rejects is replaced
func TestEnsureReplacesACachedTokenTheHubRejects(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	first, err := p.Ensure(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// the puppet was erased in the app: its name is free again
	f.puppets = map[string]string{}
	p.Check = func(_ context.Context, tok string) error {
		if tok == first {
			return withReason(connect.NewError(connect.CodeUnauthenticated, errors.New("gone")), sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID)
		}
		return nil
	}
	second, err := p.Ensure(t.Context(), dir)
	if err != nil || second == first || len(f.creates) != 2 {
		t.Fatalf("%q %v creates %v", second, err, f.creates)
	}
	// a hub that cannot be reached does not discard a good token
	p.Check = func(context.Context, string) error { return Unavailable() }
	if third, err := p.Ensure(t.Context(), dir); err != nil || third != second || len(f.creates) != 2 {
		t.Fatalf("%q %v creates %v", third, err, f.creates)
	}
}

// review F4: an empty parent is an unmounted volume on Linux, not a deleted repository
func TestSweepKeepsPuppetsWhenTheParentIsEmpty(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	mount := filepath.Join(t.TempDir(), "mnt")
	repo := filepath.Join(mount, "proj")
	_ = os.MkdirAll(repo, 0o755)
	if _, err := p.Ensure(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(repo) // the mount point is left, empty
	p.Sweep(t.Context())
	if len(f.erased) != 0 {
		t.Fatalf("erased %v", f.erased)
	}
}

// review F5: GIT_DIR of a parent process does not pick the project
func TestProjectIgnoresTheAmbientGitEnvironment(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	other := filepath.Join(root, "other")
	_ = os.Mkdir(victim, 0o755)
	_ = os.Mkdir(other, 0o755)
	git(t, victim, "init", "-q")
	t.Setenv("GIT_DIR", filepath.Join(victim, ".git"))
	got, err := Project(t.Context(), other)
	want, _ := filepath.EvalSymlinks(other)
	if err != nil || got != want {
		t.Fatalf("Project(other) = %q, %v; want %q", got, err, want)
	}
}

// review F6: a repository path with a newline still sweeps the right puppet
func TestSweepHandlesNewlinesInThePath(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	root := t.TempDir()
	dir := filepath.Join(root, "proj\nXX")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skip("filesystem refuses the name")
	}
	_ = os.WriteFile(filepath.Join(root, "keep"), nil, 0o600)
	if _, err := p.Ensure(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(dir)
	p.Sweep(t.Context())
	if len(f.erased) != 1 || f.erased[0] != "pup-b" {
		t.Fatalf("erased %v", f.erased)
	}
}

// review F7: waiting for the cache lock honours the context
func TestEnsureGivesUpOnALockedCacheWhenTheContextEnds(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	repo, _ := Project(t.Context(), dir)
	unlock, err := p.lock(t.Context(), keyOf(repo))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := p.Ensure(ctx, dir); err == nil {
		t.Fatal("ensure returned while the cache was locked")
	}
}

func TestSweepDropsOldTempFiles(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	_ = os.MkdirAll(p.Dir, 0o700)
	old := filepath.Join(p.Dir, ".tmp-123")
	_ = os.WriteFile(old, []byte("sock_pt_x_secret"), 0o600)
	when := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, when, when)
	fresh := filepath.Join(p.Dir, ".tmp-456")
	_ = os.WriteFile(fresh, nil, 0o600)
	p.Sweep(t.Context())
	if _, err := os.Stat(old); err == nil {
		t.Fatal("an old temp file with a token was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a fresh temp file (a write in progress) was removed")
	}
}

// security review 1: a machine key for one server never sends its cached
// puppet token to another server, nor reads one cached for another key
func TestProjectsDirIsScopedByServerAndMachineKey(t *testing.T) {
	state := t.TempDir()
	getenv := func(k string) string { return map[string]string{"XDG_STATE_HOME": state}[k] }
	a := Config{URL: "https://a.example", Token: "sock_mk_a_secret"}
	b := Config{URL: "https://b.example", Token: "sock_mk_a_secret"}
	k2 := Config{URL: "https://a.example", Token: "sock_mk_b_secret"}
	dirs := map[string]bool{}
	for _, c := range []Config{a, b, k2} {
		d := ProjectsDir(getenv, c)
		if !strings.HasPrefix(d, projectsBase(getenv)+string(filepath.Separator)) || strings.Contains(d, "secret") {
			t.Fatalf("dir %q", d)
		}
		dirs[d] = true
	}
	if len(dirs) != 3 || ProjectsDir(getenv, a) != ProjectsDir(getenv, Config{URL: a.URL, Token: a.Token}) {
		t.Fatalf("dirs %v", dirs)
	}

	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	fa, fb := newFake(), newFake()
	fb.next = 10 // the servers' ids differ
	pa := &Projects{API: fa, Dir: ProjectsDir(getenv, a), Host: "mac"}
	tokA, err := pa.Ensure(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// an entry in the unscoped layout of before is nobody's: never sent
	repo, _ := Project(t.Context(), dir)
	legacy := filepath.Join(projectsBase(getenv), keyOf(repo))
	if err := os.WriteFile(legacy, []byte("sock_pt_legacy_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var checked []string
	pb := &Projects{API: fb, Dir: ProjectsDir(getenv, b), Host: "mac", Check: func(_ context.Context, tok string) error {
		checked = append(checked, tok)
		return nil
	}}
	tokB, err := pb.Ensure(t.Context(), dir)
	if err != nil || tokB == tokA || len(checked) != 0 || len(fb.creates) != 1 {
		t.Fatalf("b got %q (%v), checked %v, creates %v", tokB, err, checked, fb.creates)
	}
	// b's sweep never touches a's entries
	_ = os.RemoveAll(dir)
	_ = os.WriteFile(filepath.Join(filepath.Dir(dir), "keep"), nil, 0o600)
	pb.Sweep(t.Context())
	if len(fa.erased) != 0 || len(fb.erased) != 1 {
		t.Fatalf("a erased %v, b erased %v", fa.erased, fb.erased)
	}
	if again, _ := pa.TokenFile(t.Context(), dir); !strings.HasPrefix(again, pa.Dir) {
		t.Fatalf("token file %q", again)
	}
	if _, err := os.Stat(filepath.Join(pa.Dir, keyOf(repo))); err != nil {
		t.Fatalf("a's entry was swept by b: %v", err)
	}
}

// security review 2: the token check never waits on the hub without a deadline
func TestTokenCheckHasADeadline(t *testing.T) {
	var timeout atomic.Value
	mux := http.NewServeMux()
	mux.Handle(sockv1connect.NewIdentityServiceHandler(&getMeFake{timeout: &timeout}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	check := NewTokenCheck(Config{URL: srv.URL}, "test")
	if err := check(context.Background(), "sock_pt_x_secret"); err != nil {
		t.Fatal(err)
	}
	got, _ := timeout.Load().(string)
	ms, err := strconv.Atoi(got)
	if err != nil || ms <= 0 || ms > int(UnaryTimeout/time.Millisecond) {
		t.Fatalf("Connect-Timeout-Ms %q", got)
	}
	// a rejected token still reads as TOKEN_INVALID
	if err := check(context.Background(), "sock_pt_gone_secret"); Reason(err) != sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID {
		t.Fatalf("reason of %v", err)
	}
}

type getMeFake struct {
	sockv1connect.UnimplementedIdentityServiceHandler
	timeout *atomic.Value
}

func (f *getMeFake) GetMe(_ context.Context, r *connect.Request[sockv1.GetMeRequest]) (*connect.Response[sockv1.GetMeResponse], error) {
	f.timeout.Store(r.Header().Get("Connect-Timeout-Ms"))
	if r.Header().Get("Authorization") == "Bearer sock_pt_gone_secret" {
		return nil, withReason(connect.NewError(connect.CodeUnauthenticated, errors.New("gone")), sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID)
	}
	return connect.NewResponse(&sockv1.GetMeResponse{}), nil
}

// security review 4: a repository at a volume's root looks gone when the
// volume is unmounted (its parent, /Volumes, is there and holds other
// volumes); it sat on another device than its parent, so it is not gone
func TestSweepKeepsARepositoryAtAVolumeRoot(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	volumes := t.TempDir()
	repo := filepath.Join(volumes, "X")
	_ = os.Mkdir(repo, 0o755)
	_ = os.Mkdir(filepath.Join(volumes, "Other"), 0o755)
	resolved, _ := filepath.EvalSymlinks(repo)
	old := deviceOf
	t.Cleanup(func() { deviceOf = old })
	deviceOf = func(path string) (uint64, bool) {
		if path == resolved {
			return 2, true
		}
		return 1, true
	}
	if _, err := p.Ensure(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(repo) // unmounted
	p.Sweep(t.Context())
	if len(f.erased) != 0 {
		t.Fatalf("erased %v", f.erased)
	}
	// a repository on its parent's device that vanished is gone
	deviceOf = func(string) (uint64, bool) { return 1, true }
	plain := filepath.Join(volumes, "Other", "proj")
	_ = os.Mkdir(plain, 0o755)
	_ = os.WriteFile(filepath.Join(volumes, "Other", "keep"), nil, 0o600)
	if _, err := p.Ensure(t.Context(), plain); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(plain)
	p.Sweep(t.Context())
	if len(f.erased) != 1 {
		t.Fatalf("erased %v", f.erased)
	}
}

// sock-cli-dk6: an entry of the unscoped layout is adopted, copied, when its
// puppet is one this key lists on this hub, so the upgrade mints no second
// puppet and a not-yet-upgraded reader of the old entry keeps working. Any
// other old entry is never read.
func TestEnsureAdoptsThisKeysPuppetFromTheOldLayout(t *testing.T) {
	state := t.TempDir()
	getenv := func(k string) string { return map[string]string{"XDG_STATE_HOME": state}[k] }
	cfg := Config{URL: "https://a.example", Token: "sock_mk_a_secret"}
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	repo, _ := Project(t.Context(), dir)
	base := projectsBase(getenv)
	writeLegacy := func(id, tok string) {
		t.Helper()
		_ = os.MkdirAll(base, 0o700)
		if err := os.WriteFile(filepath.Join(base, keyOf(repo)), []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, keyOf(repo)+".meta"), []byte(strconv.Quote(repo)+"\n"+id+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("this key's puppet", func(t *testing.T) {
		f := newFake()
		f.puppets["pup-old"] = "proj on mac"
		writeLegacy("pup-old", "sock_pt_pupold_secret")
		p := &Projects{API: f, Dir: ProjectsDir(getenv, cfg), Host: "mac"}
		tok, err := p.Ensure(t.Context(), dir)
		if err != nil || tok != "sock_pt_pupold_secret" || len(f.creates) != 0 {
			t.Fatalf("got %q (%v), creates %v", tok, err, f.creates)
		}
		if _, id, _ := p.meta(keyOf(repo)); id != "pup-old" || p.cached(keyOf(repo)) != tok {
			t.Fatalf("not copied into the scoped cache: id %q", id)
		}
		if b, err := os.ReadFile(filepath.Join(base, keyOf(repo))); err != nil || strings.TrimSpace(string(b)) != tok {
			t.Fatalf("the old entry was moved, not copied: %q %v", b, err)
		}
	})

	t.Run("a puppet this key does not list", func(t *testing.T) {
		f := newFake()
		writeLegacy("pup-elsewhere", "sock_pt_elsewhere_secret")
		var checked []string
		p := &Projects{API: f, Dir: ProjectsDir(getenv, Config{URL: "https://b.example", Token: cfg.Token}), Host: "mac",
			Check: func(_ context.Context, tok string) error { checked = append(checked, tok); return nil }}
		tok, err := p.Ensure(t.Context(), dir)
		if err != nil || tok == "sock_pt_elsewhere_secret" || len(f.creates) != 1 || len(checked) != 0 {
			t.Fatalf("got %q (%v), creates %v, checked %v", tok, err, f.creates, checked)
		}
	})
}

// second review 1: without HOME (Windows, a bare launcher) the cache falls
// back to USERPROFILE, and a cache dir that is still relative is refused
// rather than written into the working tree
func TestProjectsCacheIsNeverRelative(t *testing.T) {
	cfg := Config{URL: "https://a.example", Token: "sock_mk_a_secret"}
	profile := t.TempDir()
	if d := ProjectsDir(func(k string) string { return map[string]string{"USERPROFILE": profile}[k] }, cfg); !strings.HasPrefix(d, profile+string(filepath.Separator)) {
		t.Fatalf("dir %q", d)
	}
	cwd := t.TempDir()
	t.Chdir(cwd)
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	f := newFake()
	p := &Projects{API: f, Dir: ProjectsDir(func(string) string { return "" }, cfg), Host: "mac"}
	if _, err := p.Ensure(t.Context(), dir); err == nil || len(f.creates) != 0 {
		t.Fatalf("ensure with a relative cache: %v, creates %v", err, f.creates)
	}
	p.Sweep(t.Context())
	if ents, _ := os.ReadDir(cwd); len(ents) != 0 {
		t.Fatalf("wrote into the working directory: %v", ents)
	}
}

// second review 2: a stat on a hung network mount does not hold the sweep
// past its context
func TestSweepDoesNotWaitOnAHungMount(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	_ = os.Mkdir(repo, 0o755)
	_ = os.WriteFile(filepath.Join(root, "keep"), nil, 0o600)
	if _, err := p.Ensure(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(repo)
	old := deviceOf
	t.Cleanup(func() { deviceOf = old })
	hung, left := make(chan struct{}), make(chan struct{})
	// the mount comes back: the stat left behind returns before deviceOf is restored
	t.Cleanup(func() { close(hung); <-left })
	deviceOf = func(string) (uint64, bool) { <-hung; close(left); return 0, false }
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	p.Sweep(ctx)
	if took := time.Since(start); took > time.Second || len(f.erased) != 0 {
		t.Fatalf("sweep took %s, erased %v", took, f.erased)
	}
}

// second review 4: the old entry's id and token are read under the old
// layout's lock, the one a binary not yet upgraded writes them under, so a
// half-written pair is never adopted; and a listing that fails leaves the old
// puppet to adopt next time rather than making a duplicate
func TestAdoptIsLockedAndNeverDuplicatesTheOldPuppet(t *testing.T) {
	state := t.TempDir()
	getenv := func(k string) string { return map[string]string{"XDG_STATE_HOME": state}[k] }
	cfg := Config{URL: "https://a.example", Token: "sock_mk_a_secret"}
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	repo, _ := Project(t.Context(), dir)
	old := &Projects{Dir: projectsBase(getenv)}
	if err := old.prepare(); err != nil {
		t.Fatal(err)
	}
	if err := old.store(keyOf(repo), repo, "pup-old", "sock_pt_pupold_secret"); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	f.puppets["pup-old"] = "proj on mac"
	p := &Projects{API: f, Dir: ProjectsDir(getenv, cfg), Host: "mac"}

	unlock, err := old.lock(t.Context(), keyOf(repo))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if tok, err := p.Ensure(ctx, dir); err == nil || len(f.creates) != 0 {
		t.Fatalf("ensure while the old entry is locked: %q %v, creates %v", tok, err, f.creates)
	}
	unlock()

	f.failList = Unavailable()
	if tok, err := p.Ensure(t.Context(), dir); err == nil || len(f.creates) != 0 {
		t.Fatalf("ensure with the listing down: %q %v, creates %v", tok, err, f.creates)
	}
	f.failList = nil
	if tok, err := p.Ensure(t.Context(), dir); err != nil || tok != "sock_pt_pupold_secret" || len(f.creates) != 0 {
		t.Fatalf("adopt: %q %v, creates %v", tok, err, f.creates)
	}
}

// sock-mcp needs to tell a repository from a bare directory: a Codex plugin
// server starts in the plugin cache, which is no project
func TestInRepository(t *testing.T) {
	bare := t.TempDir()
	if in, err := InRepository(t.Context(), bare); err != nil || in {
		t.Fatalf("bare dir: %v, %v", in, err)
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if in, err := InRepository(t.Context(), sub); err != nil || !in {
		t.Fatalf("repo subdir: %v, %v", in, err)
	}
}

// The machine key's owner at the 100-puppet cap (ADR 10) is told what to do
// about it, and nothing is cached for the project.
func TestEnsureAtThePuppetLimitSaysWhatToEraseAndCachesNothing(t *testing.T) {
	f := newFake()
	f.failCreate = withReason(connect.NewError(connect.CodeFailedPrecondition, errors.New("limit reached: puppets per owner")), sockv1.ErrorReason_ERROR_REASON_LIMIT_REACHED)
	p := newProjects(t, f)
	dir := filepath.Join(t.TempDir(), "myproj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tok, err := p.Ensure(t.Context(), dir)
	if err == nil || tok != "" {
		t.Fatalf("token %q, err %v", tok, err)
	}
	for _, want := range []string{"puppets per owner", "sock puppets erase"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not say %q", err, want)
		}
	}
	if Reason(err) != sockv1.ErrorReason_ERROR_REASON_LIMIT_REACHED {
		t.Fatalf("reason %v", Reason(err))
	}
	if len(f.creates) != 1 || len(f.puppets) != 0 || len(f.mints) != 0 {
		t.Fatalf("creates %v, puppets %v, mints %v", f.creates, f.puppets, f.mints)
	}
	ents, _ := os.ReadDir(p.Dir)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".lock") {
			t.Fatalf("cached %s", e.Name())
		}
	}
}

// SweepEvery sweeps a cache at most once per interval: the hook calls it on
// every send (sock-cli-7ut). A stamp from the future (a clock set back) does
// not stop sweeping for good.
func TestSweepEveryRunsAtMostOncePerInterval(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	root := t.TempDir()
	gone := func(name string) {
		t.Helper()
		dir := filepath.Join(root, name)
		_ = os.Mkdir(dir, 0o755)
		if _, err := p.Ensure(t.Context(), dir); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Mkdir(filepath.Join(root, "kept"), 0o755)
	gone("one")
	p.SweepEvery(t.Context(), time.Hour)
	if len(f.erased) != 1 {
		t.Fatalf("first sweep erased %v", f.erased)
	}
	gone("two")
	p.SweepEvery(t.Context(), time.Hour)
	if len(f.erased) != 1 {
		t.Fatalf("swept again within the interval: %v", f.erased)
	}
	stamp := filepath.Join(p.Dir, ".swept")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stamp, old, old); err != nil {
		t.Fatal(err)
	}
	p.SweepEvery(t.Context(), time.Hour)
	if len(f.erased) != 2 {
		t.Fatalf("no sweep after the interval: %v", f.erased)
	}
	gone("three")
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(stamp, future, future); err != nil {
		t.Fatal(err)
	}
	p.SweepEvery(t.Context(), time.Hour)
	if len(f.erased) != 3 {
		t.Fatalf("a stamp from the future stopped the sweep: %v", f.erased)
	}
	if info, err := os.Stat(stamp); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stamp %v: %v", info, err)
	}
}

// IsProjectPuppetName knows the names Ensure gives, and only those shapes:
// sock puppets prune matches by it (sock-cli-85u)
func TestIsProjectPuppetName(t *testing.T) {
	for _, name := range []string{
		puppetName("proj", "mac"),
		puppetName("proj-"+shortHash("/x/proj"), "mac"),
		puppetName(strings.Repeat("p", 80), "build-7"),
		puppetName("my proj on fire", "mac"),
	} {
		if !IsProjectPuppetName(name) {
			t.Errorf("%q not a project puppet name", name)
		}
	}
	for _, name := range []string{
		"build-bot", "on mac", " on mac", "proj on ", "proj on my mac", "proj on mac.local",
		"proj\u200b on mac", strings.Repeat("p", 60) + " on mac",
	} {
		if IsProjectPuppetName(name) {
			t.Errorf("%q taken for a project puppet name", name)
		}
	}
}

// review: a .swept that is not a regular file (a symlink) is replaced, never
// written through
func TestSweepEveryDoesNotWriteThroughASymlink(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	if err := p.prepare(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(p.Dir, ".swept")); err != nil {
		t.Skip(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(target, old, old)
	p.SweepEvery(t.Context(), time.Hour)
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatalf("target %q: %v", b, err)
	}
	if info, err := os.Lstat(filepath.Join(p.Dir, ".swept")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("stamp %v: %v", info, err)
	}
}
