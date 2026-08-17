package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Plural holds the noun forms needed to agree with a count. English uses One
// and Many; languages with richer agreement (Russian, Polish) also use Few,
// which applies to counts ending in 2-4.
type Plural struct {
	One  string `json:"one"`
	Few  string `json:"few"`
	Many string `json:"many"`
}

func (p Plural) empty() bool {
	return p.One == "" || p.Few == "" || p.Many == ""
}

// Icons are the glyphs that mark up the parts of a message. An empty string
// drops the icon without breaking the layout of its line.
type Icons struct {
	Repo    string `json:"repo"`
	Commits string `json:"commits"`
	Bullet  string `json:"bullet"`
	Author  string `json:"author"`
	Labels  string `json:"labels"`
	Branch  string `json:"branch"`
	Time    string `json:"time"`

	Release    string `json:"release"`
	PreRelease string `json:"prerelease"`
	Tag        string `json:"tag"`

	PullRequestOpened   string `json:"pull_request_opened"`
	PullRequestMerged   string `json:"pull_request_merged"`
	PullRequestClosed   string `json:"pull_request_closed"`
	PullRequestReopened string `json:"pull_request_reopened"`

	IssueOpened   string `json:"issue_opened"`
	IssueClosed   string `json:"issue_closed"`
	IssueReopened string `json:"issue_reopened"`

	Startup string `json:"startup"`
	Error   string `json:"error"`
}

// Labels are the words the bot writes. Overriding them translates the bot or
// adapts its wording to a team's vocabulary.
type Labels struct {
	Commits Plural `json:"commits"`
	More    Plural `json:"more_commits"`
	Repos   Plural `json:"repos"`

	MorePrefix string `json:"more_prefix"`
	NoSubject  string `json:"no_subject"`

	Release    string `json:"release"`
	PreRelease string `json:"prerelease"`
	Tag        string `json:"tag"`

	PullRequest         string `json:"pull_request"`
	PullRequestOpened   string `json:"pull_request_opened"`
	PullRequestMerged   string `json:"pull_request_merged"`
	PullRequestClosed   string `json:"pull_request_closed"`
	PullRequestReopened string `json:"pull_request_reopened"`

	Issue         string `json:"issue"`
	IssueOpened   string `json:"issue_opened"`
	IssueClosed   string `json:"issue_closed"`
	IssueReopened string `json:"issue_reopened"`

	Startup         string `json:"startup"`
	StartupBot      string `json:"startup_bot"`
	StartupInterval string `json:"startup_interval"`
	StartupWatching string `json:"startup_watching"`

	Error     string `json:"error"`
	ErrorRepo string `json:"error_repo"`
}

// Messages is the complete presentation of a notification: layout, icons and
// wording. It is decoded on top of DefaultMessages, so a config file only needs
// to list the fields it changes.
type Messages struct {
	Schema string `json:"$schema"`

	Divider    string `json:"divider"`
	TimeFormat string `json:"time_format"`

	ShowFooter     bool `json:"show_footer"`
	ShowAuthor     bool `json:"show_author"`
	ShowLabels     bool `json:"show_labels"`
	ShowBody       bool `json:"show_body"`
	ExpandableBody bool `json:"expandable_body"`

	MaxSubjectLen int `json:"max_subject_len"`
	MaxTitleLen   int `json:"max_title_len"`
	MaxBodyLen    int `json:"max_body_len"`

	Icons  Icons  `json:"icons"`
	Labels Labels `json:"labels"`
}

// DefaultMessages returns the built-in English presentation, used when no
// message config file is present.
func DefaultMessages() Messages {
	return Messages{
		Divider:    "─────────────",
		TimeFormat: "02.01.2006 15:04",

		ShowFooter:     true,
		ShowAuthor:     true,
		ShowLabels:     true,
		ShowBody:       true,
		ExpandableBody: true,

		MaxSubjectLen: 140,
		MaxTitleLen:   180,
		MaxBodyLen:    600,

		Icons: Icons{
			Repo:    "📦",
			Commits: "🔨",
			Bullet:  "▸",
			Author:  "👤",
			Labels:  "🏷",
			Branch:  "🎯",
			Time:    "🕒",

			Release:    "🚀",
			PreRelease: "🧪",
			Tag:        "🏷",

			PullRequestOpened:   "🟢",
			PullRequestMerged:   "🟣",
			PullRequestClosed:   "🔴",
			PullRequestReopened: "🔄",

			IssueOpened:   "🐞",
			IssueClosed:   "✅",
			IssueReopened: "🔄",

			Startup: "🤖",
			Error:   "⚠️",
		},

		Labels: Labels{
			Commits: Plural{One: "new commit", Few: "new commits", Many: "new commits"},
			More:    Plural{One: "commit", Few: "commits", Many: "commits"},
			Repos:   Plural{One: "repository", Few: "repositories", Many: "repositories"},

			MorePrefix: "… and",
			NoSubject:  "(no message)",

			Release:    "New release",
			PreRelease: "Pre-release",
			Tag:        "New tag",

			PullRequest:         "pull request",
			PullRequestOpened:   "Opened",
			PullRequestMerged:   "Merged",
			PullRequestClosed:   "Closed",
			PullRequestReopened: "Reopened",

			Issue:         "issue",
			IssueOpened:   "Opened",
			IssueClosed:   "Closed",
			IssueReopened: "Reopened",

			Startup:         "Repository monitoring started",
			StartupBot:      "Bot:",
			StartupInterval: "Poll interval:",
			StartupWatching: "Watching:",

			Error:     "Monitoring problem",
			ErrorRepo: "Repository:",
		},
	}
}

// LoadMessages reads presentation settings from a JSON file, layering them over
// the defaults. A missing file is not an error.
func LoadMessages(path string) (*Messages, error) {
	m := DefaultMessages()
	if strings.TrimSpace(path) == "" {
		return &m, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &m, nil
		}
		return nil, fmt.Errorf("read message config %s: %w", path, err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := m.validate(path); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Messages) validate(path string) error {
	var errs []string

	if strings.TrimSpace(m.TimeFormat) == "" {
		errs = append(errs, "time_format must not be empty")
	}
	for _, limit := range []struct {
		field string
		value int
	}{
		{"max_subject_len", m.MaxSubjectLen},
		{"max_title_len", m.MaxTitleLen},
		{"max_body_len", m.MaxBodyLen},
	} {
		if limit.value < 16 {
			errs = append(errs, fmt.Sprintf("%s=%d: minimum is 16", limit.field, limit.value))
		}
	}
	for _, p := range []struct {
		field string
		value Plural
	}{
		{"labels.commits", m.Labels.Commits},
		{"labels.more_commits", m.Labels.More},
		{"labels.repos", m.Labels.Repos},
	} {
		if p.value.empty() {
			errs = append(errs, p.field+": all three forms are required (one, few, many)")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s is invalid:\n  - %s", path, strings.Join(errs, "\n  - "))
	}
	return nil
}
