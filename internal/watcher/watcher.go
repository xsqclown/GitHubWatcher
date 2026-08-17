package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/fadwix/adminbot/internal/config"
	"github.com/fadwix/adminbot/internal/github"
	"github.com/fadwix/adminbot/internal/model"
	"github.com/fadwix/adminbot/internal/render"
	"github.com/fadwix/adminbot/internal/state"
)

// Publisher delivers a rendered message. The watcher only needs this much of a
// chat client, which keeps it testable without Telegram.
type Publisher interface {
	Send(ctx context.Context, text string) error
}

const notifyEveryNFailures = 20

// Watcher polls the configured repositories on a schedule and publishes what
// changed.
type Watcher struct {
	cfg    *config.Config
	gh     *github.Client
	pub    Publisher
	rnd    *render.Renderer
	store  *state.Store
	log    *slog.Logger
	rand   *rand.Rand
	randMu sync.Mutex

	failMu   sync.Mutex
	failures map[string]int
}

// New wires a watcher together.
func New(cfg *config.Config, gh *github.Client, pub Publisher, rnd *render.Renderer, store *state.Store, log *slog.Logger) *Watcher {
	return &Watcher{
		cfg:      cfg,
		gh:       gh,
		pub:      pub,
		rnd:      rnd,
		store:    store,
		log:      log,
		rand:     rand.New(rand.NewSource(time.Now().UnixNano())),
		failures: map[string]int{},
	}
}

// Run polls until the context is cancelled, then flushes state and returns.
func (w *Watcher) Run(ctx context.Context) error {
	w.forgetRemovedRepos()

	for {
		w.cycle(ctx)

		if err := w.store.Flush(); err != nil {
			w.log.Error("failed to save state", "err", err)
		}

		delay := w.nextDelay()
		w.log.Debug("cycle finished", "next_in", delay.Truncate(time.Second))

		select {
		case <-ctx.Done():
			return w.shutdown()
		case <-time.After(delay):
		}
	}
}

func (w *Watcher) shutdown() error {
	if err := w.store.Flush(); err != nil {
		return fmt.Errorf("save state on shutdown: %w", err)
	}
	return nil
}

func (w *Watcher) forgetRemovedRepos() {
	keep := make(map[string]struct{}, len(w.cfg.Repos))
	for i := range w.cfg.Repos {
		keep[w.cfg.Repos[i].Key()] = struct{}{}
	}
	if n := w.store.Forget(keep); n > 0 {
		w.log.Info("dropped state of repositories removed from config", "count", n)
	}
}

func (w *Watcher) nextDelay() time.Duration {
	if w.cfg.Poll.Jitter <= 0 {
		return w.cfg.Poll.Interval
	}
	w.randMu.Lock()
	defer w.randMu.Unlock()
	return w.cfg.Poll.Interval + time.Duration(w.rand.Int63n(int64(w.cfg.Poll.Jitter)))
}

func (w *Watcher) cycle(ctx context.Context) {
	started := time.Now()
	sem := make(chan struct{}, w.cfg.Poll.Concurrency)

	var wg sync.WaitGroup
	for i := range w.cfg.Repos {
		repo := &w.cfg.Repos[i]

		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if rec := recover(); rec != nil {
					w.log.Error("panic while polling repository", "repo", repo.Key(), "panic", rec)
				}
			}()

			w.pollRepo(ctx, repo)
		}()
	}
	wg.Wait()

	w.log.Debug("poll finished", "repos", len(w.cfg.Repos), "took", time.Since(started).Truncate(time.Millisecond))
}

func (w *Watcher) pollRepo(ctx context.Context, repo *config.Repo) {
	log := w.log.With("repo", repo.Key(), "name", repo.Name)
	st := w.store.Repo(repo.Key())
	seeding := !st.Seeded

	if seeding {
		log.Info("first poll: recording the current position without notifying")
	}

	batches, failed := w.collectAll(ctx, repo, st, log)
	if ctx.Err() != nil {
		return
	}

	var published int
	for _, b := range batches {
		sent, err := w.publish(ctx, b, st, log)
		published += sent
		if err != nil {
			if ctx.Err() == nil {
				log.Error("failed to publish notification", "err", err)
			}
			failed = true
			break
		}
	}

	if seeding && !failed {
		st.Seeded = true
		w.store.MarkDirty()
		log.Info("repository is now being watched")
	}
	if published > 0 {
		log.Info("notifications published", "count", published)
	}
	if !failed {
		w.resetFailures(repo.Key())
	}
}

func (w *Watcher) collectAll(ctx context.Context, repo *config.Repo, st *state.Repo, log *slog.Logger) ([]batch, bool) {
	type source struct {
		kind model.Kind
		name string
		run  func() (batch, error)
	}

	var sources []source
	if repo.Watches(model.KindCommits) {
		for _, branch := range repo.Branches {
			b := branch
			name := "commits"
			if b != "" {
				name += "@" + b
			}
			sources = append(sources, source{model.KindCommits, name, func() (batch, error) {
				return w.collectCommits(ctx, repo, st, b)
			}})
		}
	}
	if repo.Watches(model.KindReleases) {
		sources = append(sources, source{model.KindReleases, "releases", func() (batch, error) {
			return w.collectReleases(ctx, repo, st)
		}})
	}
	if repo.Watches(model.KindTags) {
		sources = append(sources, source{model.KindTags, "tags", func() (batch, error) {
			return w.collectTags(ctx, repo, st)
		}})
	}
	if repo.Watches(model.KindPullRequest) {
		sources = append(sources, source{model.KindPullRequest, "pulls", func() (batch, error) {
			return w.collectPullRequests(ctx, repo, st)
		}})
	}
	if repo.Watches(model.KindIssues) {
		sources = append(sources, source{model.KindIssues, "issues", func() (batch, error) {
			return w.collectIssues(ctx, repo, st)
		}})
	}

	var (
		out    []batch
		failed bool
		tags   []string
	)

	for _, s := range sources {
		b, err := s.run()
		switch {
		case err == nil:
		case isNotModified(err):
			log.Debug("not modified", "source", s.name)
			continue
		case ctx.Err() != nil:
			return out, true
		default:
			failed = true
			w.handleSourceError(ctx, repo, s.name, err, log)
			continue
		}

		if s.kind == model.KindReleases {
			for _, p := range b.events {
				tags = append(tags, p.event.Tag)
			}
		}
		if s.kind == model.KindTags {
			b.events = dropAnnouncedTags(b.events, tags)
		}
		out = append(out, b)
	}
	return out, failed
}

func dropAnnouncedTags(events []pending, announced []string) []pending {
	if len(announced) == 0 || len(events) == 0 {
		return events
	}
	seen := make(map[string]struct{}, len(announced))
	for _, t := range announced {
		seen[t] = struct{}{}
	}
	out := events[:0]
	for _, p := range events {
		if _, dup := seen[p.event.Tag]; dup {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (w *Watcher) publish(ctx context.Context, b batch, st *state.Repo, log *slog.Logger) (int, error) {
	var sent int

	for _, p := range b.events {
		text := w.rnd.Event(p.event)
		if text == "" {
			continue
		}
		if err := w.pub.Send(ctx, text); err != nil {
			return sent, err
		}
		if p.apply != nil {
			p.apply(st)
		}
		w.store.MarkDirty()
		sent++
		log.Debug("event published", "event", p.event.Key())
	}

	if b.commitETag != nil {
		b.commitETag(st)
		w.store.MarkDirty()
	}
	return sent, nil
}

func (w *Watcher) handleSourceError(ctx context.Context, repo *config.Repo, source string, err error, log *slog.Logger) {
	var rateErr *github.RateLimitError
	if errors.As(err, &rateErr) {
		log.Warn("GitHub API rate limit exhausted", "source", source, "reset_at", rateErr.ResetAt.Format(time.RFC3339))
		return
	}

	log.Error("poll failed", "source", source, "err", err)

	count := w.bumpFailures(repo.Key())
	if count != 1 && count%notifyEveryNFailures != 0 {
		return
	}

	detail := err.Error()
	var apiErr *github.APIError
	if errors.As(err, &apiErr) && apiErr.NotFound() {
		detail = "repository unavailable: check owner/repo in the config and the token's access"
	}
	msg := w.rnd.Error(repo.Name, fmt.Sprintf("%s: %s", source, detail))

	if sendErr := w.pub.Send(ctx, msg); sendErr != nil && ctx.Err() == nil {
		log.Error("failed to report the error to the chat", "err", sendErr)
	}
}

func (w *Watcher) bumpFailures(key string) int {
	w.failMu.Lock()
	defer w.failMu.Unlock()
	w.failures[key]++
	return w.failures[key]
}

func (w *Watcher) resetFailures(key string) {
	w.failMu.Lock()
	defer w.failMu.Unlock()
	delete(w.failures, key)
}
