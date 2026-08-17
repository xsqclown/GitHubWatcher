package model

import (
	"fmt"
	"time"
)

// Kind is a category of repository event the bot can watch.
type Kind string

const (
	KindCommits     Kind = "commits"
	KindReleases    Kind = "releases"
	KindTags        Kind = "tags"
	KindPullRequest Kind = "pull_requests"
	KindIssues      Kind = "issues"
)

// AllKinds lists every supported event kind and is the default subscription.
var AllKinds = []Kind{KindCommits, KindReleases, KindTags, KindPullRequest, KindIssues}

// Valid reports whether k is a known event kind.
func (k Kind) Valid() bool {
	for _, v := range AllKinds {
		if v == k {
			return true
		}
	}
	return false
}

// RepoRef is everything a message is allowed to say about a repository. It
// deliberately holds no owner, no repo name and no URL, so no code path can
// leak a link to a private repository into a chat.
type RepoRef struct {
	Name  string
	Emoji string
}

// Title renders the repository as it appears in a message header.
func (r RepoRef) Title() string {
	if r.Emoji == "" {
		return r.Name
	}
	return r.Emoji + " " + r.Name
}

// Commit is a single commit as shown in a notification.
type Commit struct {
	SHA     string
	Message string
	Author  string
	At      time.Time
}

// ShortSHA abbreviates the hash the way git does.
func (c Commit) ShortSHA() string {
	if len(c.SHA) > 7 {
		return c.SHA[:7]
	}
	return c.SHA
}

// Action is the state change a pull request or issue went through.
type Action string

const (
	ActionOpened   Action = "opened"
	ActionMerged   Action = "merged"
	ActionClosed   Action = "closed"
	ActionReopened Action = "reopened"
)

// Event is one notification-worthy thing that happened. It is a union: which
// fields are meaningful depends on Kind.
type Event struct {
	Kind Kind
	Repo RepoRef
	At   time.Time

	Branch  string
	Commits []Commit
	More    int

	Tag        string
	Title      string
	Body       string
	PreRelease bool

	Number int
	Action Action
	Author string
	Labels []string
}

// Key identifies the event in logs, which makes duplicate deliveries visible.
func (e Event) Key() string {
	switch e.Kind {
	case KindCommits:
		if len(e.Commits) > 0 {
			return fmt.Sprintf("%s/%s/%s", e.Kind, e.Branch, e.Commits[0].SHA)
		}
		return fmt.Sprintf("%s/%s", e.Kind, e.Branch)
	case KindReleases, KindTags:
		return fmt.Sprintf("%s/%s", e.Kind, e.Tag)
	default:
		return fmt.Sprintf("%s/%d/%s", e.Kind, e.Number, e.Action)
	}
}
