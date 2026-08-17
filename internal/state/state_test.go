package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")

	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	r := store.Repo("acme/core")
	r.Seeded = true
	r.LastCommit["main"] = "abc123"
	r.LastReleaseID = 42
	r.AddTag("v1.0.0")
	r.PullRequests[7] = "merged"
	r.ETags["commits:main"] = `W/"x"`
	store.MarkDirty()

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Repo("acme/core")

	if !got.Seeded || got.LastCommit["main"] != "abc123" || got.LastReleaseID != 42 {
		t.Errorf("state was not restored: %+v", got)
	}
	if !got.HasTag("v1.0.0") || got.PullRequests[7] != "merged" || got.ETags["commits:main"] != `W/"x"` {
		t.Errorf("state was not restored: %+v", got)
	}
}

func TestFlushWritesNothingWhenClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Repo("acme/core")

	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("no file should be created while there is nothing to save")
	}
}

func TestIncompatibleFileStartsClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"repos":null}`), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Load(path)
	if err != nil {
		t.Fatalf("an incompatible version must start clean rather than fail: %v", err)
	}
	if st := store.Repo("acme/core"); st.Seeded {
		t.Error("expected empty state")
	}
}

func TestForget(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.Repo("acme/a")
	store.Repo("acme/b")

	removed := store.Forget(map[string]struct{}{"acme/a": {}})
	if removed != 1 {
		t.Fatalf("removed %d, want 1", removed)
	}
}

func TestPruneBoundsGrowth(t *testing.T) {
	r := &Repo{PullRequests: map[int]string{}, Issues: map[int]string{}}
	for i := 1; i <= maxTrackedIssues+50; i++ {
		r.PullRequests[i] = "open"
	}
	for i := 0; i < maxTrackedTags+10; i++ {
		r.AddTag("v" + string(rune('a'+i%26)) + string(rune('0'+i%10)))
	}

	r.prune()

	if len(r.PullRequests) != maxTrackedIssues {
		t.Errorf("%d pull requests left, want %d", len(r.PullRequests), maxTrackedIssues)
	}
	if _, ok := r.PullRequests[1]; ok {
		t.Error("the oldest entries should be dropped")
	}
	if _, ok := r.PullRequests[maxTrackedIssues+50]; !ok {
		t.Error("recent entries should be kept")
	}
	if len(r.KnownTags) != maxTrackedTags {
		t.Errorf("%d tags left, want %d", len(r.KnownTags), maxTrackedTags)
	}
}
