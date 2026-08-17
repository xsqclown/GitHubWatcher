package github

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

type User struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type Commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *User `json:"author"`
}

// AuthorName prefers the GitHub login and falls back to the name recorded in
// the commit itself, which is all that exists for commits pushed from an
// unlinked email address.
func (c Commit) AuthorName() string {
	if c.Author != nil && c.Author.Login != "" {
		return c.Author.Login
	}
	return c.Commit.Author.Name
}

type Release struct {
	ID          int64     `json:"id"`
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	PreRelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	CreatedAt   time.Time `json:"created_at"`
	Author      *User     `json:"author"`
}

type Tag struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type Label struct {
	Name string `json:"name"`
}

type PullRequest struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	Draft     bool       `json:"draft"`
	User      *User      `json:"user"`
	Labels    []Label    `json:"labels"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	MergedAt  *time.Time `json:"merged_at"`
	Base      struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type Issue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	User      *User      `json:"user"`
	Labels    []Label    `json:"labels"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`

	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

// IsPullRequest reports whether this "issue" is really a pull request. The
// issues endpoint returns both, so pull requests have to be filtered out.
func (i Issue) IsPullRequest() bool { return i.PullRequest != nil }

// LabelNames extracts the non-empty label names.
func LabelNames(labels []Label) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

// UserLogin returns the login of a possibly absent user.
func UserLogin(u *User) string {
	if u == nil {
		return ""
	}
	return u.Login
}

type RepoInfo struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
}

// Info fetches repository metadata. It doubles as an access check, since a
// token that cannot see the repository gets a 404 here.
func (c *Client) Info(ctx context.Context, owner, repo string) (*RepoInfo, error) {
	var out RepoInfo
	_, err := c.get(ctx, "/repos/"+owner+"/"+repo, nil, "", &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// BranchExists reports whether a branch is present, turning the expected 404
// into a false rather than an error.
func (c *Client) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	var out struct {
		Name string `json:"name"`
	}
	_, err := c.get(ctx, "/repos/"+owner+"/"+repo+"/branches/"+url.PathEscape(branch), nil, "", &out)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.NotFound() {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Commits lists the newest commits, most recent first. An empty branch means
// the repository's default branch.
func (c *Client) Commits(ctx context.Context, owner, repo, branch, etag string, limit int) ([]Commit, *Response, error) {
	q := url.Values{"per_page": {strconv.Itoa(limit)}}
	if branch != "" {
		q.Set("sha", branch)
	}
	var out []Commit
	resp, err := c.get(ctx, "/repos/"+owner+"/"+repo+"/commits", q, etag, &out)
	return out, resp, err
}

// Releases lists published releases; drafts are filtered out because they are
// not public yet.
func (c *Client) Releases(ctx context.Context, owner, repo, etag string, limit int) ([]Release, *Response, error) {
	q := url.Values{"per_page": {strconv.Itoa(limit)}}
	var raw []Release
	resp, err := c.get(ctx, "/repos/"+owner+"/"+repo+"/releases", q, etag, &raw)
	if err != nil {
		return nil, resp, err
	}
	out := raw[:0]
	for _, r := range raw {
		if !r.Draft {
			out = append(out, r)
		}
	}
	return out, resp, nil
}

// Tags lists repository tags, newest first.
func (c *Client) Tags(ctx context.Context, owner, repo, etag string, limit int) ([]Tag, *Response, error) {
	q := url.Values{"per_page": {strconv.Itoa(limit)}}
	var out []Tag
	resp, err := c.get(ctx, "/repos/"+owner+"/"+repo+"/tags", q, etag, &out)
	return out, resp, err
}

// PullRequests lists pull requests in any state, most recently updated first,
// so that closures and merges are seen as well as openings.
func (c *Client) PullRequests(ctx context.Context, owner, repo, etag string, limit int) ([]PullRequest, *Response, error) {
	q := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(limit)},
	}
	var out []PullRequest
	resp, err := c.get(ctx, "/repos/"+owner+"/"+repo+"/pulls", q, etag, &out)
	return out, resp, err
}

// Issues lists issues in any state, most recently updated first. Pull requests
// are removed from the result.
func (c *Client) Issues(ctx context.Context, owner, repo, etag string, limit int) ([]Issue, *Response, error) {
	q := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(limit)},
	}
	var raw []Issue
	resp, err := c.get(ctx, "/repos/"+owner+"/"+repo+"/issues", q, etag, &raw)
	if err != nil {
		return nil, resp, err
	}
	out := raw[:0]
	for _, i := range raw {
		if !i.IsPullRequest() {
			out = append(out, i)
		}
	}
	return out, resp, nil
}
