package render

import (
	"strings"
	"testing"
	"time"

	"github.com/fadwix/adminbot/internal/model"
)

func testRenderer() *Renderer { return New(time.UTC, 3, nil) }

func TestPlural(t *testing.T) {
	forms := Plural{One: "commit", Few: "commits", Many: "commits"}
	tests := []struct {
		n    int
		want string
	}{
		{1, "1 commit"}, {2, "2 commits"}, {5, "5 commits"}, {0, "0 commits"},
	}
	for _, tt := range tests {
		if got := plural(tt.n, forms); got != tt.want {
			t.Errorf("plural(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestPluralSlavicForms(t *testing.T) {
	forms := Plural{One: "коммит", Few: "коммита", Many: "коммитов"}
	tests := []struct {
		n    int
		want string
	}{
		{1, "1 коммит"}, {2, "2 коммита"}, {4, "4 коммита"}, {5, "5 коммитов"},
		{11, "11 коммитов"}, {12, "12 коммитов"}, {14, "14 коммитов"},
		{21, "21 коммит"}, {22, "22 коммита"}, {25, "25 коммитов"},
		{101, "101 коммит"}, {111, "111 коммитов"}, {0, "0 коммитов"},
	}
	for _, tt := range tests {
		if got := plural(tt.n, forms); got != tt.want {
			t.Errorf("plural(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 50); got != "short" {
		t.Errorf("short string was modified: %q", got)
	}
	long := strings.Repeat("a", 100)
	got := truncate(long, 10)
	if len([]rune(got)) > 10 {
		t.Errorf("length %d exceeds the limit: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected a trailing ellipsis: %q", got)
	}
}

func TestCommitsRenderWithoutLinks(t *testing.T) {
	r := testRenderer()
	ev := model.Event{
		Kind:   model.KindCommits,
		Repo:   model.RepoRef{Name: "Core", Emoji: "🧠"},
		Branch: "main",
		Commits: []model.Commit{
			{SHA: "abcdef1234567890", Message: "feat: referrals\n\ndetails", Author: "ivan"},
			{SHA: "1234567890abcdef", Message: "fix: <script>", Author: "petya"},
		},
		At: time.Date(2026, 8, 16, 14, 32, 0, 0, time.UTC),
	}

	out := r.Event(ev)

	for _, want := range []string{"🧠 Core", "<code>main</code>", "2 new commits", "<code>abcdef1</code>", "feat: referrals", "<i>ivan</i>", "16.08.2026 14:32"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "details") {
		t.Error("only the first line of a commit message belongs in the message")
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("HTML was not escaped:\n%s", out)
	}
	for _, forbidden := range []string{"github.com", "href", "http"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("messages must not contain links, found %q:\n%s", forbidden, out)
		}
	}
}

func TestCommitsRespectLimit(t *testing.T) {
	r := testRenderer()
	var commits []model.Commit
	for i := 0; i < 10; i++ {
		commits = append(commits, model.Commit{SHA: "abcdef1234", Message: "tweak", Author: "ivan"})
	}

	out := r.Event(model.Event{
		Kind:    model.KindCommits,
		Repo:    model.RepoRef{Name: "Core"},
		Branch:  "main",
		Commits: commits,
		More:    5,
	})

	if !strings.Contains(out, "15 new commits") {
		t.Errorf("the total must account for More:\n%s", out)
	}
	if !strings.Contains(out, "and 12 commits") {
		t.Errorf("expected a remainder of 12 (15 total - 3 shown):\n%s", out)
	}
	if n := strings.Count(out, "▸"); n != 3 {
		t.Errorf("rendered %d commits, want 3", n)
	}
}

func TestRenderAllEventKinds(t *testing.T) {
	r := testRenderer()
	repo := model.RepoRef{Name: "Core", Emoji: "🧠"}

	tests := []struct {
		name  string
		event model.Event
		want  []string
	}{
		{
			name:  "release",
			event: model.Event{Kind: model.KindReleases, Repo: repo, Tag: "v1.4.0", Title: "Summer", Body: "- fixes\n\n\n- more", Author: "ivan"},
			want:  []string{"🚀", "New release", "<code>v1.4.0</code>", "Summer", "<blockquote expandable>", "<i>ivan</i>"},
		},
		{
			name:  "prerelease",
			event: model.Event{Kind: model.KindReleases, Repo: repo, Tag: "v2.0.0-rc1", PreRelease: true},
			want:  []string{"🧪", "Pre-release"},
		},
		{
			name:  "tag",
			event: model.Event{Kind: model.KindTags, Repo: repo, Tag: "v1.4.1"},
			want:  []string{"🏷", "New tag", "<code>v1.4.1</code>"},
		},
		{
			name:  "pull request merged",
			event: model.Event{Kind: model.KindPullRequest, Repo: repo, Number: 42, Action: model.ActionMerged, Title: "Refactoring", Author: "ivan", Branch: "develop", Labels: []string{"backend"}},
			want:  []string{"🟣", "Merged", "<code>#42</code>", "Refactoring", "<code>develop</code>", "<code>backend</code>"},
		},
		{
			name:  "issue closed",
			event: model.Event{Kind: model.KindIssues, Repo: repo, Number: 7, Action: model.ActionClosed, Title: "Bot crashes", Author: "petya"},
			want:  []string{"✅", "Closed", "<code>#7</code>", "Bot crashes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := r.Event(tt.event)
			if out == "" {
				t.Fatal("empty output")
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output does not contain %q:\n%s", want, out)
				}
			}
			if !strings.Contains(out, "🧠 Core") {
				t.Errorf("the header must carry the repository name:\n%s", out)
			}
		})
	}
}

func TestUnknownKindRendersEmpty(t *testing.T) {
	if got := testRenderer().Event(model.Event{Kind: "something"}); got != "" {
		t.Errorf("expected an empty string, got %q", got)
	}
}

func TestStartupAndError(t *testing.T) {
	r := testRenderer()

	startup := r.Startup("fadwix_bot", []string{"Core", "Site"}, 2*time.Minute)
	for _, want := range []string{"@fadwix_bot", "2 repositories", "Core", "Site", "2m0s"} {
		if !strings.Contains(startup, want) {
			t.Errorf("startup message does not contain %q:\n%s", want, startup)
		}
	}

	errMsg := r.Error("Core", "pulls: 404 <not found>")
	if !strings.Contains(errMsg, "&lt;not found&gt;") {
		t.Errorf("error details must be escaped:\n%s", errMsg)
	}
}

func TestSubjectFallsBackWhenMessageIsBlank(t *testing.T) {
	if got := testRenderer().subject("\n\n  \n"); got != "(no message)" {
		t.Errorf("subject() = %q", got)
	}
}

func TestCustomMessagesOverrideDefaults(t *testing.T) {
	msgs := DefaultMessages()
	msgs.Divider = ""
	msgs.ShowFooter = false
	msgs.ShowAuthor = false
	msgs.Icons.Repo = ""
	msgs.Labels.Tag = "Метка"

	r := New(time.UTC, 3, &msgs)
	out := r.Event(model.Event{
		Kind: model.KindTags,
		Repo: model.RepoRef{Name: "Core"},
		Tag:  "v1.0.0",
	})

	if !strings.Contains(out, "Метка") {
		t.Errorf("custom label was not applied:\n%s", out)
	}
	if strings.Contains(out, "─") {
		t.Errorf("an empty divider must not be rendered:\n%s", out)
	}
	if strings.Contains(out, "🕒") {
		t.Errorf("the footer was disabled but is still present:\n%s", out)
	}
	if strings.HasPrefix(out, " ") {
		t.Errorf("an empty icon must not leave a leading space:\n%s", out)
	}
}
