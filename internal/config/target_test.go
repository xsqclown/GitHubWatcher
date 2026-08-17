package config

import "testing"

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Target
		wantErr bool
	}{
		{name: "direct message", in: "123456789", want: Target{ChatID: 123456789}},
		{name: "group without topics", in: "-1002233445566", want: Target{ChatID: -1002233445566}},
		{name: "forum topic", in: "-1002233445566:42", want: Target{ChatID: -1002233445566, TopicID: 42}},
		{name: "surrounding spaces", in: "  -1001 : 7 ", want: Target{ChatID: -1001, TopicID: 7}},
		{name: "empty topic is no topic", in: "-1001:", want: Target{ChatID: -1001}},
		{name: "empty string", in: "   ", wantErr: true},
		{name: "chat is not a number", in: "@channel", wantErr: true},
		{name: "topic is not a number", in: "-1001:abc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTarget(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseTarget(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestTargetString(t *testing.T) {
	if got := (Target{ChatID: -1001, TopicID: 42}).String(); got != "-1001:42" {
		t.Errorf("String() = %q", got)
	}
	if got := (Target{ChatID: 42}).String(); got != "42" {
		t.Errorf("String() = %q", got)
	}
}

func TestTargetsFromEnv(t *testing.T) {
	t.Setenv("TG_CHAT", "-1001")
	t.Setenv("TG_TOPIC", "42")
	t.Setenv("TG_TARGETS", "777, -1002, -1001:42")

	e := &envReader{}
	got := e.targets("TG_TARGETS", "TG_CHAT", "TG_TOPIC")
	if err := e.err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []Target{
		{ChatID: -1001, TopicID: 42},
		{ChatID: 777},
		{ChatID: -1002},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d recipients, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("recipient %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTargetsRejectTopicWithoutChat(t *testing.T) {
	t.Setenv("TG_TOPIC", "42")

	e := &envReader{}
	if got := e.targets("TG_TARGETS", "TG_CHAT", "TG_TOPIC"); len(got) != 0 {
		t.Fatalf("expected no recipients, got %+v", got)
	}
	if e.err() == nil {
		t.Fatal("a topic without a chat must be reported")
	}
}
