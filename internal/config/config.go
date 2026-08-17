package config

import (
	"fmt"
	"strings"
	"time"
)

// Config is the full set of bot settings: the environment plus the files it
// points at.
type Config struct {
	Telegram Telegram
	GitHub   GitHub
	Poll     Poll
	Format   Format

	ReposConfig    string
	MessagesConfig string
	StateFile      string

	LogLevel   string
	LogFormat  string
	HealthAddr string

	Repos []Repo
}

// Telegram covers notification delivery.
type Telegram struct {
	Token         string
	Targets       []Target
	SendInterval  time.Duration
	Silent        bool
	NotifyOnStart bool
}

// GitHub covers API access.
type GitHub struct {
	Token       string
	APIURL      string
	HTTPTimeout time.Duration
}

// Poll covers the polling schedule.
type Poll struct {
	Interval    time.Duration
	Jitter      time.Duration
	Concurrency int
}

// Format bounds the size of a single notification and fixes the timezone of its
// timestamps. The look of a message is configured separately, see
// Config.MessagesConfig.
type Format struct {
	MaxCommitsPerMessage int
	MaxEventsPerPoll     int
	Location             *time.Location
}

// Load reads the .env file, the environment and the repository config. All
// configuration errors are collected together so typos need not be hunted one
// restart at a time.
func Load(envPath string) (*Config, error) {
	if err := LoadDotEnv(envPath); err != nil {
		return nil, err
	}

	e := &envReader{}
	cfg := &Config{
		Telegram: Telegram{
			Token:         e.required("TELEGRAM_BOT_TOKEN"),
			Targets:       e.targets("TELEGRAM_TARGETS", "TELEGRAM_CHAT_ID", "TELEGRAM_TOPIC_ID"),
			SendInterval:  e.duration("TELEGRAM_SEND_INTERVAL", 3*time.Second),
			Silent:        e.bool("TELEGRAM_SILENT", false),
			NotifyOnStart: e.bool("TELEGRAM_NOTIFY_ON_START", true),
		},
		GitHub: GitHub{
			Token:       e.str("GITHUB_TOKEN", ""),
			APIURL:      strings.TrimRight(e.str("GITHUB_API_URL", "https://api.github.com"), "/"),
			HTTPTimeout: e.duration("HTTP_TIMEOUT", 20*time.Second),
		},
		Poll: Poll{
			Interval:    e.duration("POLL_INTERVAL", 2*time.Minute),
			Jitter:      e.duration("POLL_JITTER", 15*time.Second),
			Concurrency: e.int("POLL_CONCURRENCY", 4),
		},
		Format: Format{
			MaxCommitsPerMessage: e.int("MAX_COMMITS_PER_MESSAGE", 10),
			MaxEventsPerPoll:     e.int("MAX_EVENTS_PER_POLL", 20),
			Location:             e.location("TIMEZONE", time.UTC),
		},
		ReposConfig:    e.str("REPOS_CONFIG", "repos.json"),
		MessagesConfig: e.str("MESSAGES_CONFIG", "messages.json"),
		StateFile:      e.str("STATE_FILE", "data/state.json"),
		LogLevel:       strings.ToLower(e.str("LOG_LEVEL", "info")),
		LogFormat:      strings.ToLower(e.str("LOG_FORMAT", "text")),
		HealthAddr:     e.str("HEALTH_ADDR", ""),
	}

	cfg.validate(e)

	if err := e.err(); err != nil {
		return nil, err
	}

	repos, err := LoadRepos(cfg.ReposConfig)
	if err != nil {
		return nil, err
	}
	cfg.Repos = repos

	return cfg, nil
}

func (c *Config) validate(e *envReader) {
	if c.Telegram.Token != "" && !strings.Contains(c.Telegram.Token, ":") {
		e.fail("TELEGRAM_BOT_TOKEN does not look like a token (expected 123456:ABC-DEF...)")
	}
	if len(c.Telegram.Targets) == 0 {
		e.fail("no recipients configured: set TELEGRAM_CHAT_ID (plus TELEGRAM_TOPIC_ID for a forum) or TELEGRAM_TARGETS")
	}
	for _, t := range c.Telegram.Targets {
		if t.ChatID == 0 {
			e.fail("recipient %s: chat_id must not be zero", t)
		}
		if t.TopicID < 0 {
			e.fail("recipient %s: message_thread_id must not be negative", t)
		}
	}
	if c.Poll.Interval < 30*time.Second {
		e.fail("POLL_INTERVAL=%s is too small, the minimum is 30s (otherwise we hit GitHub API limits)", c.Poll.Interval)
	} else if c.Poll.Jitter >= c.Poll.Interval {
		e.fail("POLL_JITTER=%s must be smaller than POLL_INTERVAL=%s", c.Poll.Jitter, c.Poll.Interval)
	}
	if c.Poll.Concurrency < 1 || c.Poll.Concurrency > 32 {
		e.fail("POLL_CONCURRENCY=%d is out of range 1..32", c.Poll.Concurrency)
	}
	if c.Format.MaxCommitsPerMessage < 1 || c.Format.MaxCommitsPerMessage > 50 {
		e.fail("MAX_COMMITS_PER_MESSAGE=%d is out of range 1..50", c.Format.MaxCommitsPerMessage)
	}
	if c.Format.MaxEventsPerPoll < 1 {
		e.fail("MAX_EVENTS_PER_POLL must be >= 1")
	}
	if c.GitHub.HTTPTimeout < time.Second {
		e.fail("HTTP_TIMEOUT must be >= 1s")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		e.fail("LOG_LEVEL=%q: expected debug|info|warn|error", c.LogLevel)
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		e.fail("LOG_FORMAT=%q: expected text|json", c.LogFormat)
	}
	if c.StateFile == "" {
		e.fail("STATE_FILE must not be empty")
	}
}

// TargetList lists the recipients on one line, for logs and the -check output.
func (c *Config) TargetList() string {
	parts := make([]string, 0, len(c.Telegram.Targets))
	for _, t := range c.Telegram.Targets {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, ", ")
}

// Redacted describes the configuration in a form that is safe to log: neither
// the bot token nor the GitHub token appears in the output.
func (c *Config) Redacted() string {
	return fmt.Sprintf(
		"targets=[%s] repos=%d interval=%s concurrency=%d github_auth=%t state=%s",
		c.TargetList(), len(c.Repos),
		c.Poll.Interval, c.Poll.Concurrency, c.GitHub.Token != "", c.StateFile,
	)
}
