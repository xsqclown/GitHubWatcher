package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fadwix/adminbot/internal/config"
	"github.com/fadwix/adminbot/internal/github"
	"github.com/fadwix/adminbot/internal/render"
	"github.com/fadwix/adminbot/internal/state"
)

type fakePublisher struct {
	mu   sync.Mutex
	msgs []string
	err  error
}

func (p *fakePublisher) Send(_ context.Context, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.msgs = append(p.msgs, text)
	return nil
}

func (p *fakePublisher) taken() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.msgs
	p.msgs = nil
	return out
}

func (p *fakePublisher) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

type fakeGitHub struct {
	mu       sync.Mutex
	commits  []map[string]any
	requests int
	etag     string
}

func (f *fakeGitHub) setCommits(commits []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits = commits
	f.etag = fmt.Sprintf(`W/"%d"`, len(commits))
}

func (f *fakeGitHub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/core/commits", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		commits, etag := f.commits, f.etag
		f.requests++
		f.mu.Unlock()

		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(commits)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	return mux
}

func commit(sha, msg, author string) map[string]any {
	return map[string]any{
		"sha": sha,
		"commit": map[string]any{
			"message": msg,
			"author":  map[string]any{"name": author, "date": time.Now().UTC().Format(time.RFC3339)},
		},
		"author": map[string]any{"login": author},
	}
}

type harness struct {
	w   *Watcher
	pub *fakePublisher
	gh  *fakeGitHub
}

func newHarness(t *testing.T, reposJSON string) *harness {
	t.Helper()

	gh := &fakeGitHub{}
	gh.setCommits(nil)
	srv := httptest.NewServer(gh.handler())
	t.Cleanup(srv.Close)

	reposPath := filepath.Join(t.TempDir(), "repos.json")
	if err := os.WriteFile(reposPath, []byte(reposJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	repos, err := config.LoadRepos(reposPath)
	if err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}

	cfg := &config.Config{
		Poll:      config.Poll{Interval: time.Minute, Concurrency: 2},
		Format:    config.Format{MaxCommitsPerMessage: 10, MaxEventsPerPoll: 20, Location: time.UTC},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		Repos:     repos,
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.Load(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	pub := &fakePublisher{}

	w := New(cfg, github.New(srv.URL, "", 5*time.Second, log), pub, render.New(time.UTC, 10, nil), store, log)
	return &harness{w: w, pub: pub, gh: gh}
}

const singleRepoConfig = `{"repos": [
  {"name": "Core", "emoji": "🧠", "owner": "acme", "repo": "core", "branches": ["main"], "events": ["commits"]}
]}`

func TestFirstPollDoesNotSpam(t *testing.T) {
	h := newHarness(t, singleRepoConfig)
	h.gh.setCommits([]map[string]any{
		commit("aaa1111", "old commit", "ivan"),
		commit("bbb2222", "even older", "ivan"),
	})

	h.w.cycle(context.Background())

	if msgs := h.pub.taken(); len(msgs) != 0 {
		t.Fatalf("the first poll must not notify, got %d:\n%s", len(msgs), strings.Join(msgs, "\n---\n"))
	}
	if st := h.w.store.Repo("acme/core"); !st.Seeded || st.LastCommit["main"] != "aaa1111" {
		t.Fatalf("position was not recorded: seeded=%v last=%q", st.Seeded, st.LastCommit["main"])
	}
}

func TestNewCommitsArePublished(t *testing.T) {
	h := newHarness(t, singleRepoConfig)
	h.gh.setCommits([]map[string]any{commit("aaa1111", "old", "ivan")})
	h.w.cycle(context.Background())
	h.pub.taken()

	h.gh.setCommits([]map[string]any{
		commit("ccc3333", "feat: new feature", "petya"),
		commit("aaa1111", "old", "ivan"),
	})
	h.w.cycle(context.Background())

	msgs := h.pub.taken()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0], "feat: new feature") {
		t.Errorf("the new commit is missing from the message:\n%s", msgs[0])
	}
	if strings.Contains(msgs[0], "old") {
		t.Errorf("an already-published commit was repeated:\n%s", msgs[0])
	}
	if !strings.Contains(msgs[0], "🧠 Core") {
		t.Errorf("the repository name is missing:\n%s", msgs[0])
	}
	if strings.Contains(msgs[0], "acme/core") {
		t.Errorf("the repository path leaked into the message:\n%s", msgs[0])
	}

	h.w.cycle(context.Background())
	if msgs := h.pub.taken(); len(msgs) != 0 {
		t.Fatalf("no changes must produce no messages, got %d", len(msgs))
	}
}

func TestFailedSendIsRetriedNextCycle(t *testing.T) {
	h := newHarness(t, singleRepoConfig)
	h.gh.setCommits([]map[string]any{commit("aaa1111", "old", "ivan")})
	h.w.cycle(context.Background())
	h.pub.taken()

	h.pub.setErr(errors.New("telegram unavailable"))
	h.gh.setCommits([]map[string]any{
		commit("ccc3333", "important commit", "petya"),
		commit("aaa1111", "old", "ivan"),
	})
	h.w.cycle(context.Background())

	if st := h.w.store.Repo("acme/core"); st.LastCommit["main"] == "ccc3333" {
		t.Fatal("progress must not be committed until the message is delivered")
	}

	h.pub.setErr(nil)
	h.w.cycle(context.Background())

	msgs := h.pub.taken()
	if len(msgs) == 0 {
		t.Fatal("the event was lost: it must be delivered once the link recovers")
	}
	if !strings.Contains(msgs[len(msgs)-1], "important commit") {
		t.Errorf("wrong commit:\n%s", msgs[len(msgs)-1])
	}
}

func TestIgnoredAuthorsAreSkipped(t *testing.T) {
	h := newHarness(t, `{"repos": [
	  {"name": "Core", "owner": "acme", "repo": "core", "branches": ["main"],
	   "events": ["commits"], "ignore_authors": ["dependabot[bot]"]}
	]}`)
	h.gh.setCommits([]map[string]any{commit("aaa1111", "old", "ivan")})
	h.w.cycle(context.Background())
	h.pub.taken()

	h.gh.setCommits([]map[string]any{
		commit("ddd4444", "chore: bump deps", "dependabot[bot]"),
		commit("aaa1111", "old", "ivan"),
	})
	h.w.cycle(context.Background())

	if msgs := h.pub.taken(); len(msgs) != 0 {
		t.Fatalf("a commit by an ignored author must not notify:\n%s", strings.Join(msgs, "\n"))
	}
	if st := h.w.store.Repo("acme/core"); st.LastCommit["main"] != "ddd4444" {
		t.Errorf("the position must advance even without a notification, got %q", st.LastCommit["main"])
	}
}

func TestMissingRepositoryIsReportedOnce(t *testing.T) {
	h := newHarness(t, `{"repos": [
	  {"name": "Vanished", "owner": "acme", "repo": "missing", "branches": ["main"], "events": ["commits"]}
	]}`)

	h.w.cycle(context.Background())
	msgs := h.pub.taken()
	if len(msgs) != 1 {
		t.Fatalf("expected a single error message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0], "Vanished") || !strings.Contains(msgs[0], "check owner/repo") {
		t.Errorf("unhelpful error message:\n%s", msgs[0])
	}

	h.w.cycle(context.Background())
	if msgs := h.pub.taken(); len(msgs) != 0 {
		t.Fatalf("a repeated failure must not repeat the message, got %d", len(msgs))
	}
}

func TestForcePushDoesNotLoseCommits(t *testing.T) {
	h := newHarness(t, singleRepoConfig)
	h.gh.setCommits([]map[string]any{commit("aaa1111", "old", "ivan")})
	h.w.cycle(context.Background())
	h.pub.taken()

	h.gh.setCommits([]map[string]any{
		commit("eee5555", "rewritten history", "ivan"),
		commit("fff6666", "and its base", "ivan"),
	})
	h.w.cycle(context.Background())

	msgs := h.pub.taken()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0], "rewritten history") {
		t.Errorf("new commits must appear in the message:\n%s", msgs[0])
	}
}
