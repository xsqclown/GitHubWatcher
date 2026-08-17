package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotEnvLine(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		key     string
		val     string
		ok      bool
		wantErr bool
	}{
		{name: "plain pair", in: "TOKEN=abc", key: "TOKEN", val: "abc", ok: true},
		{name: "surrounding spaces", in: "  TOKEN = abc  ", key: "TOKEN", val: "abc", ok: true},
		{name: "export prefix", in: "export TOKEN=abc", key: "TOKEN", val: "abc", ok: true},
		{name: "comment", in: "# comment", ok: false},
		{name: "blank line", in: "   ", ok: false},
		{name: "inline comment", in: "A=1 # note", key: "A", val: "1", ok: true},
		{name: "hash without space is kept", in: "A=1#2", key: "A", val: "1#2", ok: true},
		{name: "double quotes", in: `A="hello world"`, key: "A", val: "hello world", ok: true},
		{name: "escapes inside quotes", in: `A="a\nb"`, key: "A", val: "a\nb", ok: true},
		{name: "single quotes are literal", in: `A='a\nb # x'`, key: "A", val: `a\nb # x`, ok: true},
		{name: "empty value", in: "A=", key: "A", val: "", ok: true},
		{name: "token with a colon", in: "T=123:AA-bb_cc", key: "T", val: "123:AA-bb_cc", ok: true},
		{name: "no equals sign", in: "JUST_TEXT", wantErr: true},
		{name: "invalid name", in: "A-B=1", wantErr: true},
		{name: "leading digit", in: "1A=1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, val, ok, err := parseDotEnvLine(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && (key != tt.key || val != tt.val) {
				t.Fatalf("got %q=%q, want %q=%q", key, val, tt.key, tt.val)
			}
		})
	}
}

func TestLoadDotEnvDoesNotOverrideEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "FROM_FILE=file\nALREADY_SET=file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ALREADY_SET", "env")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv("FROM_FILE") })

	if got := os.Getenv("FROM_FILE"); got != "file" {
		t.Errorf("FROM_FILE = %q, want %q", got, "file")
	}
	if got := os.Getenv("ALREADY_SET"); got != "env" {
		t.Errorf("the real environment must win, got %q", got)
	}
}

func TestLoadDotEnvSkipsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("\ufeffWITH_BOM=ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv("WITH_BOM") })

	if got := os.Getenv("WITH_BOM"); got != "ok" {
		t.Fatalf("WITH_BOM = %q, want %q", got, "ok")
	}
}

func TestLoadDotEnvMissingFileIsNotAnError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
