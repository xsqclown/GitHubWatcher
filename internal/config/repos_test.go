package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fadwix/adminbot/internal/model"
)

func writeRepos(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReposAppliesDefaults(t *testing.T) {
	path := writeRepos(t, `{
	  "defaults": {"branches": ["main"], "events": ["commits"]},
	  "repos": [
	    {"name": "Core", "owner": "acme", "repo": "core"},
	    {"name": "Site", "emoji": "🌐", "owner": "acme", "repo": "site", "events": ["releases"]}
	  ]
	}`)

	repos, err := LoadRepos(path)
	if err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repositories, want 2", len(repos))
	}

	core := repos[0]
	if got := core.Branches; len(got) != 1 || got[0] != "main" {
		t.Errorf("default branches were not applied: %v", got)
	}
	if !core.Watches(model.KindCommits) || core.Watches(model.KindIssues) {
		t.Error("default events were not applied")
	}
	if repos[1].Watches(model.KindCommits) || !repos[1].Watches(model.KindReleases) {
		t.Error("explicit events must override the defaults")
	}
	if got := repos[1].Ref().Title(); got != "🌐 Site" {
		t.Errorf("Title() = %q", got)
	}
	if got := core.Key(); got != "acme/core" {
		t.Errorf("Key() = %q", got)
	}
}

func TestLoadReposSkipsDisabled(t *testing.T) {
	path := writeRepos(t, `{"repos": [
	  {"name": "On", "owner": "a", "repo": "b"},
	  {"name": "Off", "owner": "a", "repo": "c", "enabled": false}
	]}`)

	repos, err := LoadRepos(path)
	if err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "On" {
		t.Fatalf("expected only the enabled repository, got %+v", repos)
	}
}

func TestLoadReposErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "missing name",
			content: `{"repos": [{"owner": "a", "repo": "b"}]}`,
			want:    "name is required",
		},
		{
			name:    "missing owner",
			content: `{"repos": [{"name": "X", "repo": "b"}]}`,
			want:    "owner and repo are required",
		},
		{
			name:    "duplicate repository",
			content: `{"repos": [{"name": "X", "owner": "a", "repo": "b"}, {"name": "Y", "owner": "a", "repo": "b"}]}`,
			want:    "already declared",
		},
		{
			name:    "duplicate name",
			content: `{"repos": [{"name": "X", "owner": "a", "repo": "b"}, {"name": "x", "owner": "a", "repo": "c"}]}`,
			want:    "already used",
		},
		{
			name:    "unknown event kind",
			content: `{"repos": [{"name": "X", "owner": "a", "repo": "b", "events": ["comits"]}]}`,
			want:    "unknown event kind",
		},
		{
			name:    "typo in a key",
			content: `{"repos": [{"name": "X", "owner": "a", "repo": "b", "branch": ["main"]}]}`,
			want:    "unknown field",
		},
		{
			name:    "nothing enabled",
			content: `{"repos": [{"name": "X", "owner": "a", "repo": "b", "enabled": false}]}`,
			want:    "no enabled repositories",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadRepos(writeRepos(t, tt.content))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestRepoIgnored(t *testing.T) {
	path := writeRepos(t, `{"repos": [
	  {"name": "X", "owner": "a", "repo": "b", "ignore_authors": ["Dependabot[bot]"]}
	]}`)

	repos, err := LoadRepos(path)
	if err != nil {
		t.Fatal(err)
	}
	r := repos[0]

	if !r.Ignored("dependabot[bot]") {
		t.Error("author matching must be case-insensitive")
	}
	if r.Ignored("human") || r.Ignored("") {
		t.Error("an ordinary author must not be ignored")
	}
}

func TestExampleRepoConfigIsValid(t *testing.T) {
	if _, err := LoadRepos(filepath.Join("..", "..", "repos.example.json")); err != nil {
		t.Fatalf("repos.example.json is invalid: %v", err)
	}
}
