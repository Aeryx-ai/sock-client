package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
	// failErase, when set, answers ErasePuppet with it
	failErase error
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

// a lost cache (or a lost response) leaves our own puppet behind; take it back
// with a new token instead of making a second one
func TestEnsureReclaimsItsOwnPuppetAfterACacheLoss(t *testing.T) {
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
	if err != nil || second == first || len(f.mints) != 1 || len(f.puppets) != 1 {
		t.Fatalf("second %q (%v), mints %v, puppets %v", second, err, f.mints, f.puppets)
	}
	if again, _ := p.Ensure(t.Context(), dir); again != second {
		t.Fatalf("not cached after reclaiming: %q", again)
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

func TestEnsureErrorsNeverCarryTheToken(t *testing.T) {
	f := newFake()
	p := newProjects(t, f)
	p.API = errAPI{fakePuppets: f, err: errors.New("boom sock_pt_x_secret")}
	dir := filepath.Join(t.TempDir(), "proj")
	_ = os.Mkdir(dir, 0o755)
	_, err := p.Ensure(t.Context(), dir)
	if err == nil {
		t.Fatal("no error")
	}
}

type errAPI struct {
	*fakePuppets
	err error
}

func (e errAPI) CreatePuppet(context.Context, string, string) (string, string, error) {
	return "", "", e.err
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

func TestDefaultProjectsDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := DefaultProjectsDir(env(map[string]string{"XDG_STATE_HOME": "/x"})); got != "/x/sock-integrations/puppets" {
		t.Fatal(got)
	}
	if got := DefaultProjectsDir(env(map[string]string{"HOME": "/h"})); got != "/h/.local/state/sock-integrations/puppets" {
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
}

func (f *identityFake) CreatePuppet(_ context.Context, r *connect.Request[sockv1.CreatePuppetRequest]) (*connect.Response[sockv1.CreatePuppetResponse], error) {
	f.auth = append(f.auth, r.Header().Get("Authorization"))
	f.create = r.Msg
	return connect.NewResponse(&sockv1.CreatePuppetResponse{Puppet: &sockv1.Account{Id: "p1"}, Token: "sock_pt_d_s"}), nil
}

func (f *identityFake) ErasePuppet(_ context.Context, r *connect.Request[sockv1.ErasePuppetRequest]) (*connect.Response[sockv1.ErasePuppetResponse], error) {
	f.auth = append(f.auth, r.Header().Get("Authorization"))
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
