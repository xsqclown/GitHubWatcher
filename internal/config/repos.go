package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fadwix/adminbot/internal/model"
)

// Repo is one watched repository. Name is what the chat sees; Owner and Repo
// stay on the server side and never reach a message.
type Repo struct {
	Name  string `json:"name"`
	Emoji string `json:"emoji"`

	Owner string `json:"owner"`
	Repo  string `json:"repo"`

	Branches      []string     `json:"branches"`
	Events        []model.Kind `json:"events"`
	IgnoreAuthors []string     `json:"ignore_authors"`

	Enabled *bool `json:"enabled"`

	ignoreSet map[string]struct{}
	eventSet  map[model.Kind]struct{}
}

type reposFile struct {
	Schema   string `json:"$schema"`
	Defaults struct {
		Branches      []string     `json:"branches"`
		Events        []model.Kind `json:"events"`
		IgnoreAuthors []string     `json:"ignore_authors"`
	} `json:"defaults"`
	Repos []Repo `json:"repos"`
}

// Key identifies the repository in state and logs as "owner/repo".
func (r *Repo) Key() string { return r.Owner + "/" + r.Repo }

// Ref returns the public face of the repository: only the parts that are safe
// to put in a message.
func (r *Repo) Ref() model.RepoRef { return model.RepoRef{Name: r.Name, Emoji: r.Emoji} }

// Watches reports whether this repository subscribes to the given event kind.
func (r *Repo) Watches(k model.Kind) bool {
	_, ok := r.eventSet[k]
	return ok
}

// Ignored reports whether events authored by this login should be dropped,
// which is how bot accounts are kept out of the chat.
func (r *Repo) Ignored(author string) bool {
	if author == "" {
		return false
	}
	_, ok := r.ignoreSet[strings.ToLower(author)]
	return ok
}

// LoadRepos reads the repository config, applies the defaults block and
// validates every entry. Disabled repositories are dropped; an empty result is
// an error, since a bot with nothing to watch is a misconfiguration.
func LoadRepos(path string) ([]Repo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read repository config %s: %w", path, err)
	}

	var file reposFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(file.Defaults.Events) == 0 {
		file.Defaults.Events = model.AllKinds
	}
	if len(file.Defaults.Branches) == 0 {
		file.Defaults.Branches = []string{""}
	}

	var (
		out  []Repo
		errs []string
		seen = map[string]int{}
		used = map[string]int{}
	)

	for i := range file.Repos {
		r := file.Repos[i]
		where := fmt.Sprintf("repos[%d]", i)

		r.Name = strings.TrimSpace(r.Name)
		r.Owner = strings.TrimSpace(r.Owner)
		r.Repo = strings.TrimSpace(r.Repo)
		r.Emoji = strings.TrimSpace(r.Emoji)

		if r.Name == "" {
			errs = append(errs, where+": name is required")
		}
		if r.Owner == "" || r.Repo == "" {
			errs = append(errs, where+": owner and repo are required")
		}
		if r.Owner != "" && r.Repo != "" {
			if prev, dup := seen[r.Key()]; dup {
				errs = append(errs, fmt.Sprintf("%s: repository %s is already declared in repos[%d]", where, r.Key(), prev))
			}
			seen[r.Key()] = i
		}
		if r.Name != "" {
			if prev, dup := used[strings.ToLower(r.Name)]; dup {
				errs = append(errs, fmt.Sprintf("%s: name %q is already used by repos[%d] — names must be distinguishable", where, r.Name, prev))
			}
			used[strings.ToLower(r.Name)] = i
		}

		if len(r.Branches) == 0 {
			r.Branches = file.Defaults.Branches
		}
		if len(r.Events) == 0 {
			r.Events = file.Defaults.Events
		}
		if len(r.IgnoreAuthors) == 0 {
			r.IgnoreAuthors = file.Defaults.IgnoreAuthors
		}

		r.eventSet = make(map[model.Kind]struct{}, len(r.Events))
		for _, k := range r.Events {
			if !k.Valid() {
				errs = append(errs, fmt.Sprintf("%s: unknown event kind %q (allowed: %s)", where, k, joinKinds(model.AllKinds)))
				continue
			}
			r.eventSet[k] = struct{}{}
		}

		r.ignoreSet = make(map[string]struct{}, len(r.IgnoreAuthors))
		for _, a := range r.IgnoreAuthors {
			r.ignoreSet[strings.ToLower(strings.TrimSpace(a))] = struct{}{}
		}

		if r.Enabled != nil && !*r.Enabled {
			continue
		}
		out = append(out, r)
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("%s is invalid:\n  - %s", path, strings.Join(errs, "\n  - "))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no enabled repositories", path)
	}
	return out, nil
}

func joinKinds(kinds []model.Kind) string {
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}
