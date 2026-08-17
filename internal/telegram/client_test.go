package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSplitMessageKeepsShortTextIntact(t *testing.T) {
	parts := splitMessage("hello", 100)
	if len(parts) != 1 || parts[0] != "hello" {
		t.Fatalf("got %#v", parts)
	}
}

func TestSplitMessageBreaksOnLineBoundaries(t *testing.T) {
	line := strings.Repeat("a", 50)
	text := strings.Repeat(line+"\n", 10)

	parts := splitMessage(text, 120)
	if len(parts) < 4 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	for i, p := range parts {
		if n := len([]rune(p)); n > 120 {
			t.Errorf("part %d is %d runes, over the limit", i, n)
		}

		for _, l := range strings.Split(p, "\n") {
			if l != "" && l != line {
				t.Errorf("part %d contains a torn line: %q", i, l)
			}
		}
	}
}

func TestSplitMessageChunksLongLineByRunes(t *testing.T) {
	text := strings.Repeat("я", 250)

	parts := splitMessage(text, 100)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	if joined := strings.Join(parts, ""); joined != text {
		t.Error("re-joined parts must equal the original text")
	}
	for i, p := range parts {
		if n := len([]rune(p)); n > 100 {
			t.Errorf("part %d is %d runes, over the limit", i, n)
		}
	}
}

func TestAPIErrorFatal(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{401, true}, {403, true}, {400, true}, {404, true},
		{429, false}, {500, false}, {502, false},
	}
	for _, tt := range tests {
		e := &APIError{Code: tt.code}
		if got := e.Fatal(); got != tt.want {
			t.Errorf("code %d: Fatal() = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestRedactTokenStripsTokenFromError(t *testing.T) {
	const token = "123456:SECRET"
	err := redactToken(errStub("Post https://api.telegram.org/bot123456:SECRET/sendMessage: timeout"), token)
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("expected redaction: %v", err)
	}
}

func TestTargetString(t *testing.T) {
	if got := (Target{ChatID: -1001, TopicID: 42}).String(); got != "-1001:42" {
		t.Errorf("String() = %q", got)
	}
	if got := (Target{ChatID: 777}).String(); got != "777" {
		t.Errorf("String() = %q", got)
	}
}

type sentMessage struct {
	ChatID          int64  `json:"chat_id"`
	MessageThreadID int64  `json:"message_thread_id"`
	Text            string `json:"text"`
}

// fakeTelegram records sendMessage payloads and lets a test fail a chosen chat.
type fakeTelegram struct {
	mu       sync.Mutex
	sent     []sentMessage
	failChat int64
	failCode int
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	var msg sentMessage
	_ = json.Unmarshal(body, &msg)

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failChat != 0 && msg.ChatID == f.failChat {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  f.failCode,
			"description": "forbidden: bot was blocked by the user",
		})
		return
	}

	f.sent = append(f.sent, msg)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
}

func (f *fakeTelegram) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func newTestClient(t *testing.T, fake *fakeTelegram, targets []Target) *Client {
	t.Helper()

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := New(Options{
		Token:   "123:ABC",
		Targets: targets,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	c.http = srv.Client()
	c.baseURL = srv.URL
	return c
}

func TestSendFansOutToEveryTarget(t *testing.T) {
	fake := &fakeTelegram{}
	targets := []Target{
		{ChatID: 777},
		{ChatID: -1001, TopicID: 42},
		{ChatID: -1002},
	}
	c := newTestClient(t, fake, targets)

	if err := c.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent := fake.messages()
	if len(sent) != 3 {
		t.Fatalf("delivered %d messages, want 3", len(sent))
	}

	got := map[int64]int64{}
	for _, m := range sent {
		got[m.ChatID] = m.MessageThreadID
		if m.Text != "hello" {
			t.Errorf("chat %d received %q", m.ChatID, m.Text)
		}
	}
	if got[777] != 0 {
		t.Errorf("a direct message must not carry message_thread_id, got %d", got[777])
	}
	if got[-1001] != 42 {
		t.Errorf("the forum target must carry topic 42, got %d", got[-1001])
	}
	if _, ok := got[-1002]; !ok {
		t.Error("the plain group did not receive the message")
	}
}

func TestSendDisablesTargetAfterFatalError(t *testing.T) {
	fake := &fakeTelegram{failChat: 777, failCode: http.StatusForbidden}
	c := newTestClient(t, fake, []Target{{ChatID: 777}, {ChatID: -1002}})

	if err := c.Send(context.Background(), "first"); err != nil {
		t.Fatalf("one blocked recipient must not fail the whole send: %v", err)
	}
	if err := c.Send(context.Background(), "second"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent := fake.messages()
	if len(sent) != 2 {
		t.Fatalf("delivered %d messages, want 2", len(sent))
	}
	for _, m := range sent {
		if m.ChatID == 777 {
			t.Error("a disabled recipient must not be retried")
		}
	}
	if !c.isBroken(Target{ChatID: 777}) {
		t.Error("the blocked recipient should be marked broken")
	}
}

func TestSendFailsWhenNothingCouldBeDelivered(t *testing.T) {
	fake := &fakeTelegram{failChat: 777, failCode: http.StatusForbidden}
	c := newTestClient(t, fake, []Target{{ChatID: 777}})

	if err := c.Send(context.Background(), "first"); err == nil {
		t.Fatal("a send that reached nobody must return an error")
	}
	if err := c.Send(context.Background(), "second"); err == nil {
		t.Fatal("with no reachable recipients Send must keep returning an error")
	}
	if n := len(fake.messages()); n != 0 {
		t.Fatalf("nothing should have been delivered, got %d", n)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
