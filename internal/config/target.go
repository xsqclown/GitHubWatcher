package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Target is a notification recipient: a chat and, for forum supergroups, a
// topic inside it. A direct message, a plain group and a forum topic differ
// only in these two numbers, so one type covers all three.
type Target struct {
	ChatID  int64
	TopicID int64
}

// String renders the recipient the way it is written in TELEGRAM_TARGETS:
// "-1001234567890:42" or "123456789".
func (t Target) String() string {
	if t.TopicID != 0 {
		return strconv.FormatInt(t.ChatID, 10) + ":" + strconv.FormatInt(t.TopicID, 10)
	}
	return strconv.FormatInt(t.ChatID, 10)
}

// ParseTarget parses a single entry of the form "chat_id" or "chat_id:topic_id".
func ParseTarget(s string) (Target, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Target{}, fmt.Errorf("empty recipient")
	}

	chatPart, topicPart := raw, ""
	if i := strings.LastIndexByte(raw, ':'); i > 0 {
		chatPart, topicPart = raw[:i], raw[i+1:]
	}

	chatID, err := strconv.ParseInt(strings.TrimSpace(chatPart), 10, 64)
	if err != nil {
		return Target{}, fmt.Errorf("recipient %q: %q is not a chat_id", raw, chatPart)
	}

	var topicID int64
	if topicPart = strings.TrimSpace(topicPart); topicPart != "" {
		topicID, err = strconv.ParseInt(topicPart, 10, 64)
		if err != nil {
			return Target{}, fmt.Errorf("recipient %q: %q is not a message_thread_id", raw, topicPart)
		}
	}

	return Target{ChatID: chatID, TopicID: topicID}, nil
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	})
}
