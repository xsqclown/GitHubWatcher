package render

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/fadwix/adminbot/internal/model"
)

const maxErrorLen = 500

// Renderer builds the HTML text of Telegram notifications. It never emits a
// link to a repository by design: only the display name from the config leaves
// this package.
type Renderer struct {
	loc        *time.Location
	maxCommits int
	m          *Messages
}

// New creates a Renderer. A nil loc or msgs falls back to the defaults.
func New(loc *time.Location, maxCommits int, msgs *Messages) *Renderer {
	if loc == nil {
		loc = time.UTC
	}
	if maxCommits <= 0 {
		maxCommits = 10
	}
	if msgs == nil {
		def := DefaultMessages()
		msgs = &def
	}
	return &Renderer{loc: loc, maxCommits: maxCommits, m: msgs}
}

// Event turns an event into a ready-to-send message. An unknown kind yields an
// empty string, which callers skip instead of sending.
func (r *Renderer) Event(e model.Event) string {
	switch e.Kind {
	case model.KindCommits:
		return r.commits(e)
	case model.KindReleases:
		return r.release(e)
	case model.KindTags:
		return r.tag(e)
	case model.KindPullRequest:
		return r.pullRequest(e)
	case model.KindIssues:
		return r.issue(e)
	default:
		return ""
	}
}

// Startup announces what the bot is watching when it comes up.
func (r *Renderer) Startup(botUsername string, repos []string, interval time.Duration) string {
	var b strings.Builder

	r.title(&b, r.m.Icons.Startup, r.m.Labels.Startup)
	line(&b, "%s <code>@%s</code>", esc(r.m.Labels.StartupBot), esc(botUsername))
	line(&b, "%s <code>%s</code>", esc(r.m.Labels.StartupInterval), esc(interval.String()))
	line(&b, "%s <b>%s</b>", esc(r.m.Labels.StartupWatching), plural(len(repos), r.m.Labels.Repos))
	b.WriteByte('\n')
	for _, name := range repos {
		line(&b, "%s%s", bullet(r.m.Icons.Bullet), esc(name))
	}

	r.footer(&b, time.Now())
	return b.String()
}

// Error reports a polling failure for one repository to the chat.
func (r *Renderer) Error(repoName, detail string) string {
	var b strings.Builder

	r.title(&b, r.m.Icons.Error, r.m.Labels.Error)
	if repoName != "" {
		line(&b, "%s <b>%s</b>", esc(r.m.Labels.ErrorRepo), esc(repoName))
	}
	line(&b, "<code>%s</code>", esc(truncate(detail, maxErrorLen)))

	r.footer(&b, time.Now())
	return b.String()
}

func (r *Renderer) commits(e model.Event) string {
	var b strings.Builder
	r.head(&b, e.Repo, e.Branch)

	total := len(e.Commits) + e.More
	r.badge(&b, r.m.Icons.Commits, plural(total, r.m.Labels.Commits), "")
	b.WriteByte('\n')

	shown := e.Commits
	if len(shown) > r.maxCommits {
		shown = shown[:r.maxCommits]
	}
	hidden := total - len(shown)

	for _, c := range shown {
		line(&b, "%s<code>%s</code> %s", bullet(r.m.Icons.Bullet), esc(c.ShortSHA()), esc(r.subject(c.Message)))
		if r.m.ShowAuthor {
			if author := strings.TrimSpace(c.Author); author != "" {
				line(&b, "   <i>%s</i>", esc(author))
			}
		}
	}
	if hidden > 0 {
		line(&b, "\n<i>%s %s</i>", esc(r.m.Labels.MorePrefix), plural(hidden, r.m.Labels.More))
	}

	r.footer(&b, e.At)
	return b.String()
}

func (r *Renderer) release(e model.Event) string {
	var b strings.Builder
	r.head(&b, e.Repo, "")

	icon, label := r.m.Icons.Release, r.m.Labels.Release
	if e.PreRelease {
		icon, label = r.m.Icons.PreRelease, r.m.Labels.PreRelease
	}
	r.badge(&b, icon, label, " <code>"+esc(e.Tag)+"</code>")

	if title := strings.TrimSpace(e.Title); title != "" && title != e.Tag {
		line(&b, "<b>%s</b>", esc(truncate(title, r.m.MaxTitleLen)))
	}
	r.author(&b, e.Author)

	if r.m.ShowBody {
		if body := r.cleanBody(e.Body); body != "" {
			b.WriteByte('\n')
			line(&b, "<blockquote%s>%s</blockquote>", expandable(r.m.ExpandableBody), esc(body))
		}
	}

	r.footer(&b, e.At)
	return b.String()
}

func (r *Renderer) tag(e model.Event) string {
	var b strings.Builder
	r.head(&b, e.Repo, "")
	r.badge(&b, r.m.Icons.Tag, r.m.Labels.Tag, " <code>"+esc(e.Tag)+"</code>")
	r.footer(&b, e.At)
	return b.String()
}

func (r *Renderer) pullRequest(e model.Event) string {
	var b strings.Builder
	r.head(&b, e.Repo, "")

	icon, label := r.prBadge(e.Action)
	r.badge(&b, icon, label, fmt.Sprintf(" · %s <code>#%d</code>", esc(r.m.Labels.PullRequest), e.Number))
	line(&b, "%s", esc(truncate(e.Title, r.m.MaxTitleLen)))

	var meta []string
	if r.m.ShowAuthor && e.Author != "" {
		meta = append(meta, withIcon(r.m.Icons.Author, "<i>"+esc(e.Author)+"</i>"))
	}
	if e.Branch != "" {
		meta = append(meta, withIcon(r.m.Icons.Branch, "<code>"+esc(e.Branch)+"</code>"))
	}
	if len(meta) > 0 {
		line(&b, "%s", strings.Join(meta, "  "))
	}
	r.labels(&b, e.Labels)

	r.footer(&b, e.At)
	return b.String()
}

func (r *Renderer) issue(e model.Event) string {
	var b strings.Builder
	r.head(&b, e.Repo, "")

	icon, label := r.issueBadge(e.Action)
	r.badge(&b, icon, label, fmt.Sprintf(" · %s <code>#%d</code>", esc(r.m.Labels.Issue), e.Number))
	line(&b, "%s", esc(truncate(e.Title, r.m.MaxTitleLen)))

	r.author(&b, e.Author)
	r.labels(&b, e.Labels)

	r.footer(&b, e.At)
	return b.String()
}

func (r *Renderer) head(b *strings.Builder, ref model.RepoRef, branch string) {
	head := withIcon(r.m.Icons.Repo, "<b>"+esc(ref.Title())+"</b>")
	if branch != "" {
		head += " · <code>" + esc(branch) + "</code>"
	}
	line(b, "%s", head)
	r.divider(b)
}

func (r *Renderer) title(b *strings.Builder, icon, text string) {
	line(b, "%s", withIcon(icon, "<b>"+esc(text)+"</b>"))
	r.divider(b)
}

func (r *Renderer) divider(b *strings.Builder) {
	if r.m.Divider != "" {
		line(b, "%s", r.m.Divider)
	}
}

func (r *Renderer) badge(b *strings.Builder, icon, label, suffix string) {
	line(b, "%s%s", withIcon(icon, "<b>"+esc(label)+"</b>"), suffix)
}

func (r *Renderer) author(b *strings.Builder, name string) {
	if !r.m.ShowAuthor {
		return
	}
	if name = strings.TrimSpace(name); name == "" {
		return
	}
	line(b, "%s", withIcon(r.m.Icons.Author, "<i>"+esc(name)+"</i>"))
}

func (r *Renderer) labels(b *strings.Builder, labels []string) {
	if !r.m.ShowLabels || len(labels) == 0 {
		return
	}
	parts := make([]string, 0, len(labels))
	for _, l := range labels {
		parts = append(parts, "<code>"+esc(l)+"</code>")
	}
	line(b, "%s", withIcon(r.m.Icons.Labels, strings.Join(parts, " ")))
}

func (r *Renderer) footer(b *strings.Builder, at time.Time) {
	if !r.m.ShowFooter {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	line(b, "\n<i>%s</i>", withIcon(r.m.Icons.Time, at.In(r.loc).Format(r.m.TimeFormat)))
}

func (r *Renderer) prBadge(a model.Action) (string, string) {
	switch a {
	case model.ActionMerged:
		return r.m.Icons.PullRequestMerged, r.m.Labels.PullRequestMerged
	case model.ActionClosed:
		return r.m.Icons.PullRequestClosed, r.m.Labels.PullRequestClosed
	case model.ActionReopened:
		return r.m.Icons.PullRequestReopened, r.m.Labels.PullRequestReopened
	default:
		return r.m.Icons.PullRequestOpened, r.m.Labels.PullRequestOpened
	}
}

func (r *Renderer) issueBadge(a model.Action) (string, string) {
	switch a {
	case model.ActionClosed:
		return r.m.Icons.IssueClosed, r.m.Labels.IssueClosed
	case model.ActionReopened:
		return r.m.Icons.IssueReopened, r.m.Labels.IssueReopened
	default:
		return r.m.Icons.IssueOpened, r.m.Labels.IssueOpened
	}
}

func (r *Renderer) subject(msg string) string {
	for _, l := range strings.Split(msg, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			return truncate(s, r.m.MaxSubjectLen)
		}
	}
	return r.m.Labels.NoSubject
}

func (r *Renderer) cleanBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var out []string
	var blank bool
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return truncate(strings.TrimSpace(strings.Join(out, "\n")), r.m.MaxBodyLen)
}

func line(b *strings.Builder, format string, args ...any) {
	fmt.Fprintf(b, format, args...)
	b.WriteByte('\n')
}

func esc(s string) string { return html.EscapeString(s) }

func withIcon(icon, text string) string {
	if icon == "" {
		return text
	}
	return icon + " " + text
}

func bullet(icon string) string {
	if icon == "" {
		return ""
	}
	return icon + " "
}

func expandable(on bool) string {
	if on {
		return " expandable"
	}
	return ""
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	cut := limit - 1

	for i := cut; i > cut-20 && i > 0; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ,.;:—-") + "…"
}

func plural(n int, p Plural) string {
	word := p.Many
	switch mod100 := n % 100; {
	case mod100 >= 11 && mod100 <= 14:
	default:
		switch n % 10 {
		case 1:
			word = p.One
		case 2, 3, 4:
			word = p.Few
		}
	}
	return fmt.Sprintf("%d %s", n, word)
}
