package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type envReader struct {
	errs []string
}

func (e *envReader) fail(format string, args ...any) {
	e.errs = append(e.errs, fmt.Sprintf(format, args...))
}

func (e *envReader) err() error {
	if len(e.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(e.errs, "\n  - "))
}

func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, ok
}

func (e *envReader) str(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

func (e *envReader) required(key string) string {
	v, ok := lookup(key)
	if !ok {
		e.fail("%s is required but not set", key)
	}
	return v
}

func (e *envReader) duration(key string, def time.Duration) time.Duration {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.fail("%s=%q: not a duration (examples: 30s, 2m, 1h)", key, v)
		return def
	}
	if d < 0 {
		e.fail("%s=%q: must not be negative", key, v)
		return def
	}
	return d
}

func (e *envReader) int(key string, def int) int {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.fail("%s=%q: not an integer", key, v)
		return def
	}
	return n
}

func (e *envReader) int64(key string, def int64) int64 {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		e.fail("%s=%q: not an integer", key, v)
		return def
	}
	return n
}

func (e *envReader) targets(listKey, chatKey, topicKey string) []Target {
	var (
		out  []Target
		seen = map[Target]struct{}{}
	)
	add := func(t Target) {
		if _, dup := seen[t]; dup {
			return
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}

	if _, ok := lookup(chatKey); ok {
		add(Target{ChatID: e.int64(chatKey, 0), TopicID: e.int64(topicKey, 0)})
	} else if _, ok := lookup(topicKey); ok {
		e.fail("%s is set without %s: a topic on its own has no chat to post to", topicKey, chatKey)
	}

	if raw, ok := lookup(listKey); ok {
		for _, item := range splitList(raw) {
			t, err := ParseTarget(item)
			if err != nil {
				e.fail("%s: %v", listKey, err)
				continue
			}
			add(t)
		}
	}

	return out
}

func (e *envReader) bool(key string, def bool) bool {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail("%s=%q: not a boolean (true/false/1/0)", key, v)
		return def
	}
	return b
}

func (e *envReader) location(key string, def *time.Location) *time.Location {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	loc, err := time.LoadLocation(v)
	if err != nil {
		e.fail("%s=%q: unknown timezone (%v)", key, v, err)
		return def
	}
	return loc
}
