package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata"

	"github.com/fadwix/adminbot/internal/config"
	"github.com/fadwix/adminbot/internal/github"
	"github.com/fadwix/adminbot/internal/model"
	"github.com/fadwix/adminbot/internal/render"
	"github.com/fadwix/adminbot/internal/state"
	"github.com/fadwix/adminbot/internal/telegram"
	"github.com/fadwix/adminbot/internal/watcher"
)

var version = "dev"

func main() {
	var (
		envPath  = flag.String("env", ".env", "path to the environment file")
		check    = flag.Bool("check", false, "validate the configuration and access, then exit")
		showVer  = flag.Bool("version", false, "print the version and exit")
		resetAll = flag.Bool("reset-state", false, "delete the state file before starting (repositories are reseeded silently)")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("FadwixAdminBot", version)
		return
	}

	if err := run(*envPath, *check, *resetAll); err != nil {
		fmt.Fprintln(os.Stderr, "fatal: "+err.Error())
		os.Exit(1)
	}
}

func run(envPath string, checkOnly, resetState bool) error {
	cfg, err := config.Load(envPath)
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel, cfg.LogFormat)
	log.Info("starting GitHubWatcher", "version", version, "config", cfg.Redacted())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	msgs, err := render.LoadMessages(cfg.MessagesConfig)
	if err != nil {
		return err
	}

	tg := telegram.New(telegram.Options{
		Token:    cfg.Telegram.Token,
		Targets:  telegramTargets(cfg),
		Silent:   cfg.Telegram.Silent,
		Interval: cfg.Telegram.SendInterval,
		Timeout:  cfg.GitHub.HTTPTimeout,
		Logger:   log.With("component", "telegram"),
	})

	verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	bot, err := tg.VerifyBot(verifyCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("verify bot: %w", err)
	}
	log.Info("bot authorised", "username", bot.Username, "id", bot.ID, "targets", cfg.TargetList())

	if resetState {
		if err := os.Remove(cfg.StateFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete state: %w", err)
		}
		log.Warn("state reset", "file", cfg.StateFile)
	}

	store, err := state.Load(cfg.StateFile)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(cfg.Repos))
	for i := range cfg.Repos {
		names = append(names, cfg.Repos[i].Ref().Title())
	}
	log.Info("watched repositories", "count", len(names), "list", strings.Join(names, ", "))

	gh := github.New(cfg.GitHub.APIURL, cfg.GitHub.Token, cfg.GitHub.HTTPTimeout, log.With("component", "github"))
	if cfg.GitHub.Token == "" {
		log.Warn("GITHUB_TOKEN is not set: limited to 60 requests per hour, private repositories are unreachable")
	}

	if checkOnly {
		return checkRepoAccess(ctx, gh, cfg)
	}

	rnd := render.New(cfg.Format.Location, cfg.Format.MaxCommitsPerMessage, msgs)

	if cfg.Telegram.NotifyOnStart {
		if err := tg.Send(ctx, rnd.Startup(bot.Username, names, cfg.Poll.Interval)); err != nil {
			log.Error("failed to send the startup message", "err", err)
		}
	}

	if cfg.HealthAddr != "" {
		go serveHealth(ctx, cfg.HealthAddr, log)
	}

	w := watcher.New(cfg, gh, tg, rnd, store, log.With("component", "watcher"))
	if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	log.Info("stopped cleanly")
	return nil
}

func telegramTargets(cfg *config.Config) []telegram.Target {
	out := make([]telegram.Target, 0, len(cfg.Telegram.Targets))
	for _, t := range cfg.Telegram.Targets {
		out = append(out, telegram.Target{ChatID: t.ChatID, TopicID: t.TopicID})
	}
	return out
}

func checkRepoAccess(ctx context.Context, gh *github.Client, cfg *config.Config) error {
	fmt.Println("\nNotification recipients:")
	for _, t := range cfg.Telegram.Targets {
		fmt.Printf("  • %s\n", describeTarget(t))
	}

	fmt.Println("\nRepository access:")

	var failed int
	for i := range cfg.Repos {
		repo := &cfg.Repos[i]

		info, err := gh.Info(ctx, repo.Owner, repo.Repo)
		if err != nil {
			failed++
			fmt.Printf("  ✗ %s — %s: %s\n", repo.Name, repo.Key(), accessHint(err))
			continue
		}

		access := "public"
		if info.Private {
			access = "private"
		}
		if info.Archived {
			access += ", archived"
		}
		fmt.Printf("  ✓ %s — %s (%s, default branch %s)\n", repo.Name, info.FullName, access, info.DefaultBranch)

		if !repo.Watches(model.KindCommits) {
			continue
		}
		for _, branch := range repo.Branches {
			if branch == "" {
				continue
			}
			exists, err := gh.BranchExists(ctx, repo.Owner, repo.Repo, branch)
			switch {
			case err != nil:
				failed++
				fmt.Printf("      ✗ could not check branch %q: %v\n", branch, err)
			case !exists:
				failed++
				fmt.Printf("      ✗ branch %q not found (the default branch is %q)\n", branch, info.DefaultBranch)
			default:
				fmt.Printf("      ✓ branch %q\n", branch)
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("check failed: %d problem(s)", failed)
	}
	fmt.Println("Check passed: the configuration is valid and every repository is reachable.")
	return nil
}

func describeTarget(t config.Target) string {
	switch {
	case t.TopicID > 0:
		return fmt.Sprintf("chat %d, topic %d", t.ChatID, t.TopicID)
	case t.ChatID > 0:
		return fmt.Sprintf("direct messages, chat_id %d", t.ChatID)
	default:
		return fmt.Sprintf("chat %d (no topics)", t.ChatID)
	}
}

func accessHint(err error) string {
	var apiErr *github.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.NotFound():
			return "not found — check owner/repo; for a private organisation repository " +
				"the token must have access to it (and be SSO-authorised)"
		case apiErr.StatusCode == 401:
			return "GITHUB_TOKEN is invalid"
		case apiErr.StatusCode == 403:
			return "forbidden — the token is not approved by the organisation or lacks permissions"
		}
	}
	return err.Error()
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func serveHealth(ctx context.Context, addr string, log *slog.Logger) {
	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	}
	mux.HandleFunc("/healthz", handler)
	mux.HandleFunc("/readyz", handler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("healthcheck listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("healthcheck stopped", "err", err)
	}
}
