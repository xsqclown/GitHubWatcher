package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMessages(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "messages.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMessagesMissingFileUsesDefaults(t *testing.T) {
	m, err := LoadMessages(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if m.Labels.Release != DefaultMessages().Labels.Release {
		t.Errorf("expected the defaults, got %q", m.Labels.Release)
	}
}

func TestLoadMessagesMergesOverDefaults(t *testing.T) {
	path := writeMessages(t, `{
	  "divider": "***",
	  "labels": { "release": "Свежий релиз" }
	}`)

	m, err := LoadMessages(path)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	if m.Divider != "***" {
		t.Errorf("divider = %q, want %q", m.Divider, "***")
	}
	if m.Labels.Release != "Свежий релиз" {
		t.Errorf("labels.release = %q", m.Labels.Release)
	}
	if m.Labels.Tag != DefaultMessages().Labels.Tag {
		t.Errorf("fields absent from the file must keep their default, got %q", m.Labels.Tag)
	}
	if !m.ShowFooter {
		t.Error("show_footer must stay enabled when the file does not mention it")
	}
	if m.MaxBodyLen != DefaultMessages().MaxBodyLen {
		t.Errorf("max_body_len = %d, want the default", m.MaxBodyLen)
	}
}

func TestLoadMessagesMergesNestedObjects(t *testing.T) {
	path := writeMessages(t, `{"labels": {"commits": {"one": "commit"}}}`)

	m, err := LoadMessages(path)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if m.Labels.Commits.One != "commit" {
		t.Errorf("one = %q, want the override", m.Labels.Commits.One)
	}
	if m.Labels.Commits.Many != DefaultMessages().Labels.Commits.Many {
		t.Errorf("many = %q, want the default to survive a partial override", m.Labels.Commits.Many)
	}
}

func TestLoadMessagesRejectsUnknownField(t *testing.T) {
	path := writeMessages(t, `{"divder": "***"}`)

	if _, err := LoadMessages(path); err == nil {
		t.Fatal("a typo in a field name must be reported, not ignored")
	}
}

func TestLoadMessagesValidates(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty time format", `{"time_format": " "}`, "time_format"},
		{"limit too small", `{"max_title_len": 4}`, "max_title_len"},
		{"blanked plural form", `{"labels": {"commits": {"many": ""}}}`, "labels.commits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadMessages(writeMessages(t, tt.body))
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error does not mention %q: %v", tt.want, err)
			}
		})
	}
}

func TestExampleMessagesConfigIsValid(t *testing.T) {
	for _, path := range []string{"../../messages.example.json", "../../messages.ru.json"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s must be part of the repository: %v", path, err)
		}
		if _, err := LoadMessages(path); err != nil {
			t.Errorf("%s is invalid: %v", path, err)
		}
	}
}
