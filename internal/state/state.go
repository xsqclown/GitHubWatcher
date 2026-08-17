package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Version is the state file schema version. A file written by a different
// version is discarded rather than migrated: the bot simply reseeds, which
// costs nothing but a silent first poll.
const Version = 1

const (
	maxTrackedTags   = 200
	maxTrackedIssues = 300
)

// Repo is the remembered position of one repository: what has already been
// announced, so a restart does not replay old events.
type Repo struct {
	Seeded bool `json:"seeded"`

	LastCommit map[string]string `json:"last_commit,omitempty"`

	LastReleaseID int64 `json:"last_release_id,omitempty"`

	KnownTags []string `json:"known_tags,omitempty"`

	PullRequests map[int]string `json:"pull_requests,omitempty"`
	Issues       map[int]string `json:"issues,omitempty"`

	ETags map[string]string `json:"etags,omitempty"`
}

type file struct {
	Version int              `json:"version"`
	Repos   map[string]*Repo `json:"repos"`
}

// Store is the concurrency-safe owner of the state file. It writes only when
// something actually changed and only via a temp file plus rename, so a crash
// mid-write cannot leave a truncated state behind.
type Store struct {
	path string

	mu    sync.Mutex
	data  file
	dirty bool
}

// Load reads the state file. A missing or unreadable-as-current-version file
// yields an empty store rather than an error.
func Load(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: file{Version: Version, Repos: map[string]*Repo{}},
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}

	var loaded file
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, fmt.Errorf("parse state %s: %w (delete the file to start over)", path, err)
	}
	if loaded.Version != Version || loaded.Repos == nil {
		return s, nil
	}
	s.data = loaded
	return s, nil
}

// Repo returns the state of one repository, creating it on first use with all
// maps initialised.
func (s *Store) Repo(key string) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.data.Repos[key]
	if !ok {
		r = &Repo{}
		s.data.Repos[key] = r
	}
	if r.LastCommit == nil {
		r.LastCommit = map[string]string{}
	}
	if r.PullRequests == nil {
		r.PullRequests = map[int]string{}
	}
	if r.Issues == nil {
		r.Issues = map[int]string{}
	}
	if r.ETags == nil {
		r.ETags = map[string]string{}
	}
	return r
}

// Forget drops the state of repositories no longer in the config and returns
// how many were removed.
func (s *Store) Forget(keep map[string]struct{}) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed int
	for key := range s.data.Repos {
		if _, ok := keep[key]; !ok {
			delete(s.data.Repos, key)
			removed++
			s.dirty = true
		}
	}
	return removed
}

// MarkDirty records that the in-memory state diverged from the file. Callers
// mark only after a notification was delivered, so a failed send is retried
// instead of being silently forgotten.
func (s *Store) MarkDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// Flush writes the state to disk if anything changed since the last write.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		return nil
	}
	for _, r := range s.data.Repos {
		r.prune()
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := writeAtomic(s.path, raw); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// AddTag records a tag as announced.
func (r *Repo) AddTag(tag string) { r.KnownTags = append(r.KnownTags, tag) }

// HasTag reports whether a tag was already announced.
func (r *Repo) HasTag(tag string) bool {
	for _, t := range r.KnownTags {
		if t == tag {
			return true
		}
	}
	return false
}

func (r *Repo) prune() {
	if n := len(r.KnownTags); n > maxTrackedTags {
		r.KnownTags = append([]string(nil), r.KnownTags[n-maxTrackedTags:]...)
	}
	pruneNumbers(r.PullRequests, maxTrackedIssues)
	pruneNumbers(r.Issues, maxTrackedIssues)
}

func pruneNumbers(m map[int]string, maxKeep int) {
	if len(m) <= maxKeep {
		return
	}
	nums := make([]int, 0, len(m))
	for n := range m {
		nums = append(nums, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	for _, n := range nums[maxKeep:] {
		delete(m, n)
	}
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync state to disk: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return fmt.Errorf("replace state file: %w", err)
		}
		if err := os.Rename(tmpName, path); err != nil {
			return fmt.Errorf("replace state file: %w", err)
		}
	}
	return nil
}
