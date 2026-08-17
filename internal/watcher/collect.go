package watcher

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/fadwix/adminbot/internal/config"
	"github.com/fadwix/adminbot/internal/github"
	"github.com/fadwix/adminbot/internal/model"
	"github.com/fadwix/adminbot/internal/state"
)

type pending struct {
	event model.Event
	apply func(*state.Repo)
}

type batch struct {
	events []pending

	commitETag func(*state.Repo)
}

var errNotModified = github.ErrNotModified

func (w *Watcher) collectCommits(ctx context.Context, r *config.Repo, st *state.Repo, branch string) (batch, error) {
	key := "commits:" + branch
	commits, resp, err := w.gh.Commits(ctx, r.Owner, r.Repo, branch, st.ETags[key], w.pageSize())
	if err != nil {
		return batch{}, err
	}
	setETag := etagSetter(key, resp)

	if len(commits) == 0 {
		return batch{commitETag: setETag}, nil
	}

	newest := commits[0].SHA
	last := st.LastCommit[branch]

	fresh := commits
	for i, c := range commits {
		if c.SHA == last {
			fresh = commits[:i]
			break
		}
	}

	if !st.Seeded {
		return batch{commitETag: func(s *state.Repo) {
			s.LastCommit[branch] = newest
			setETag(s)
		}}, nil
	}

	filtered := make([]model.Commit, 0, len(fresh))
	for _, c := range fresh {
		if r.Ignored(c.AuthorName()) {
			continue
		}
		filtered = append(filtered, model.Commit{
			SHA:     c.SHA,
			Message: c.Commit.Message,
			Author:  c.AuthorName(),
			At:      c.Commit.Author.Date,
		})
	}
	if len(filtered) == 0 {
		return batch{commitETag: func(s *state.Repo) {
			s.LastCommit[branch] = newest
			setETag(s)
		}}, nil
	}

	reverse(filtered)

	var more int
	if len(filtered) > w.cfg.Format.MaxEventsPerPoll {
		more = len(filtered) - w.cfg.Format.MaxEventsPerPoll
		filtered = filtered[more:]
	}

	ev := model.Event{
		Kind:    model.KindCommits,
		Repo:    r.Ref(),
		Branch:  branch,
		Commits: filtered,
		More:    more,
		At:      filtered[len(filtered)-1].At,
	}

	return batch{
		events: []pending{{
			event: ev,
			apply: func(s *state.Repo) { s.LastCommit[branch] = newest },
		}},
		commitETag: setETag,
	}, nil
}

func (w *Watcher) collectReleases(ctx context.Context, r *config.Repo, st *state.Repo) (batch, error) {
	const key = "releases"
	releases, resp, err := w.gh.Releases(ctx, r.Owner, r.Repo, st.ETags[key], 20)
	if err != nil {
		return batch{}, err
	}
	setETag := etagSetter(key, resp)

	var maxID int64
	for _, rel := range releases {
		maxID = max64(maxID, rel.ID)
	}

	if !st.Seeded {
		return batch{commitETag: func(s *state.Repo) {
			s.LastReleaseID = maxID
			setETag(s)
		}}, nil
	}

	fresh := make([]github.Release, 0, len(releases))
	for _, rel := range releases {
		if rel.ID > st.LastReleaseID && !r.Ignored(github.UserLogin(rel.Author)) {
			fresh = append(fresh, rel)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].ID < fresh[j].ID })
	fresh = tail(fresh, w.cfg.Format.MaxEventsPerPoll)

	out := make([]pending, 0, len(fresh))
	for _, rel := range fresh {
		id := rel.ID
		out = append(out, pending{
			event: model.Event{
				Kind:       model.KindReleases,
				Repo:       r.Ref(),
				Tag:        rel.TagName,
				Title:      rel.Name,
				Body:       rel.Body,
				PreRelease: rel.PreRelease,
				Author:     github.UserLogin(rel.Author),
				At:         releaseTime(rel),
			},
			apply: func(s *state.Repo) { s.LastReleaseID = max64(s.LastReleaseID, id) },
		})
	}

	if len(out) == 0 && maxID > st.LastReleaseID {
		prevSet := setETag
		setETag = func(s *state.Repo) {
			s.LastReleaseID = max64(s.LastReleaseID, maxID)
			prevSet(s)
		}
	}

	return batch{events: out, commitETag: setETag}, nil
}

func (w *Watcher) collectTags(ctx context.Context, r *config.Repo, st *state.Repo) (batch, error) {
	const key = "tags"
	tags, resp, err := w.gh.Tags(ctx, r.Owner, r.Repo, st.ETags[key], 50)
	if err != nil {
		return batch{}, err
	}
	setETag := etagSetter(key, resp)

	if !st.Seeded {
		return batch{commitETag: func(s *state.Repo) {
			for _, t := range tags {
				if !s.HasTag(t.Name) {
					s.AddTag(t.Name)
				}
			}
			setETag(s)
		}}, nil
	}

	fresh := make([]github.Tag, 0)
	for _, t := range tags {
		if !st.HasTag(t.Name) {
			fresh = append(fresh, t)
		}
	}

	reverse(fresh)
	fresh = tail(fresh, w.cfg.Format.MaxEventsPerPoll)

	out := make([]pending, 0, len(fresh))
	for _, t := range fresh {
		name := t.Name
		out = append(out, pending{
			event: model.Event{
				Kind: model.KindTags,
				Repo: r.Ref(),
				Tag:  name,
				At:   time.Now(),
			},
			apply: func(s *state.Repo) {
				if !s.HasTag(name) {
					s.AddTag(name)
				}
			},
		})
	}
	return batch{events: out, commitETag: setETag}, nil
}

func (w *Watcher) collectPullRequests(ctx context.Context, r *config.Repo, st *state.Repo) (batch, error) {
	const key = "pulls"
	prs, resp, err := w.gh.PullRequests(ctx, r.Owner, r.Repo, st.ETags[key], 30)
	if err != nil {
		return batch{}, err
	}
	setETag := etagSetter(key, resp)

	if !st.Seeded {
		return batch{commitETag: func(s *state.Repo) {
			for _, pr := range prs {
				s.PullRequests[pr.Number] = prStatus(pr)
			}
			setETag(s)
		}}, nil
	}

	type change struct {
		pr     github.PullRequest
		action model.Action
		status string
	}
	var changes []change

	for _, pr := range prs {
		status := prStatus(pr)
		prev, known := st.PullRequests[pr.Number]
		if known && prev == status {
			continue
		}
		if r.Ignored(github.UserLogin(pr.User)) {
			st.PullRequests[pr.Number] = status
			continue
		}
		changes = append(changes, change{pr: pr, action: transition(known, prev, status), status: status})
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].pr.UpdatedAt.Before(changes[j].pr.UpdatedAt)
	})
	changes = tail(changes, w.cfg.Format.MaxEventsPerPoll)

	out := make([]pending, 0, len(changes))
	for _, ch := range changes {
		num, status := ch.pr.Number, ch.status
		out = append(out, pending{
			event: model.Event{
				Kind:   model.KindPullRequest,
				Repo:   r.Ref(),
				Number: num,
				Action: ch.action,
				Title:  ch.pr.Title,
				Author: github.UserLogin(ch.pr.User),
				Branch: ch.pr.Base.Ref,
				Labels: github.LabelNames(ch.pr.Labels),
				At:     ch.pr.UpdatedAt,
			},
			apply: func(s *state.Repo) { s.PullRequests[num] = status },
		})
	}
	return batch{events: out, commitETag: setETag}, nil
}

func (w *Watcher) collectIssues(ctx context.Context, r *config.Repo, st *state.Repo) (batch, error) {
	const key = "issues"
	issues, resp, err := w.gh.Issues(ctx, r.Owner, r.Repo, st.ETags[key], 30)
	if err != nil {
		return batch{}, err
	}
	setETag := etagSetter(key, resp)

	if !st.Seeded {
		return batch{commitETag: func(s *state.Repo) {
			for _, is := range issues {
				s.Issues[is.Number] = is.State
			}
			setETag(s)
		}}, nil
	}

	type change struct {
		issue  github.Issue
		action model.Action
	}
	var changes []change

	for _, is := range issues {
		prev, known := st.Issues[is.Number]
		if known && prev == is.State {
			continue
		}
		if r.Ignored(github.UserLogin(is.User)) {
			st.Issues[is.Number] = is.State
			continue
		}
		changes = append(changes, change{issue: is, action: transition(known, prev, is.State)})
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].issue.UpdatedAt.Before(changes[j].issue.UpdatedAt)
	})
	changes = tail(changes, w.cfg.Format.MaxEventsPerPoll)

	out := make([]pending, 0, len(changes))
	for _, ch := range changes {
		num, status := ch.issue.Number, ch.issue.State
		out = append(out, pending{
			event: model.Event{
				Kind:   model.KindIssues,
				Repo:   r.Ref(),
				Number: num,
				Action: ch.action,
				Title:  ch.issue.Title,
				Author: github.UserLogin(ch.issue.User),
				Labels: github.LabelNames(ch.issue.Labels),
				At:     ch.issue.UpdatedAt,
			},
			apply: func(s *state.Repo) { s.Issues[num] = status },
		})
	}
	return batch{events: out, commitETag: setETag}, nil
}

func transition(known bool, prev, cur string) model.Action {
	switch {
	case !known:
		if cur == "open" {
			return model.ActionOpened
		}
		return model.Action(cur)
	case cur == "open":
		return model.ActionReopened
	case cur == "merged":
		return model.ActionMerged
	default:
		return model.ActionClosed
	}
}

func prStatus(pr github.PullRequest) string {
	switch {
	case pr.MergedAt != nil:
		return "merged"
	case pr.State == "closed":
		return "closed"
	default:
		return "open"
	}
}

func releaseTime(r github.Release) time.Time {
	if !r.PublishedAt.IsZero() {
		return r.PublishedAt
	}
	if !r.CreatedAt.IsZero() {
		return r.CreatedAt
	}
	return time.Now()
}

func etagSetter(key string, resp *github.Response) func(*state.Repo) {
	if resp == nil || resp.ETag == "" {
		return func(*state.Repo) {}
	}
	etag := resp.ETag
	return func(s *state.Repo) { s.ETags[key] = etag }
}

func isNotModified(err error) bool { return errors.Is(err, errNotModified) }

func (w *Watcher) pageSize() int {
	n := w.cfg.Format.MaxEventsPerPoll + 5
	if n < 30 {
		n = 30
	}
	if n > 100 {
		n = 100
	}
	return n
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func tail[T any](s []T, n int) []T {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
