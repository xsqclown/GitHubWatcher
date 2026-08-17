package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs from a .env file into the process
// environment. A missing file is not an error, and variables already present in
// the real environment are never overwritten — that keeps container and CI
// settings authoritative over a file left on disk.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if line == 1 {
			text = strings.TrimPrefix(text, "\ufeff")
		}

		key, val, ok, err := parseDotEnvLine(text)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("%s:%d: set %s: %w", path, line, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func parseDotEnvLine(raw string) (key, val string, ok bool, err error) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = strings.TrimPrefix(line, "export ")

	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", "", false, fmt.Errorf("missing '=' in line %q", raw)
	}

	key = strings.TrimSpace(line[:eq])
	if key == "" {
		return "", "", false, fmt.Errorf("empty variable name in line %q", raw)
	}
	if !validKey(key) {
		return "", "", false, fmt.Errorf("invalid variable name %q", key)
	}

	val = strings.TrimSpace(line[eq+1:])
	switch {
	case len(val) >= 2 && val[0] == '"' && strings.HasSuffix(val, `"`):
		val = unescape(val[1 : len(val)-1])
	case len(val) >= 2 && val[0] == '\'' && strings.HasSuffix(val, `'`):
		val = val[1 : len(val)-1]
	default:
		val = stripInlineComment(val)
	}
	return key, val, true, nil
}

func validKey(k string) bool {
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func stripInlineComment(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	if i := strings.Index(v, "\t#"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

func unescape(v string) string {
	return strings.NewReplacer(
		`\n`, "\n",
		`\r`, "\r",
		`\t`, "\t",
		`\"`, `"`,
		`\\`, `\`,
	).Replace(v)
}
