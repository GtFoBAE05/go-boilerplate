package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "" +
		"# a comment\n" +
		"\n" +
		"export LOG_FORMAT=json\n" +
		`POSTGRES_DSN="host=localhost password='s3 cret'"` + "\n" +
		"REDIS_PASSWORD=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	got, err := ParseDotEnv(path)
	if err != nil {
		t.Fatalf("ParseDotEnv: %v", err)
	}
	want := map[string]string{
		"LOG_FORMAT":     "json",
		"POSTGRES_DSN":   "host=localhost password='s3 cret'",
		"REDIS_PASSWORD": "",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d entries, got %d (%v)", len(want), len(got), got)
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Errorf("%s: want %q, got %q", key, wantValue, got[key])
		}
	}
}

// Parsing must not touch the process environment: that would make config
// loading non-idempotent and leak across tests sharing a process.
func TestParseDotEnvDoesNotMutateEnvironment(t *testing.T) {
	const key = "LOG_FORMAT"
	before, hadBefore := os.LookupEnv(key)

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(key+"=json\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if _, err := ParseDotEnv(path); err != nil {
		t.Fatalf("ParseDotEnv: %v", err)
	}

	after, hadAfter := os.LookupEnv(key)
	if hadBefore != hadAfter || before != after {
		t.Fatalf("ParseDotEnv mutated the environment: %s went from (%q,%v) to (%q,%v)",
			key, before, hadBefore, after, hadAfter)
	}
}

// A missing .env is the normal production case, not an error.
func TestParseDotEnvMissingFileIsNotAnError(t *testing.T) {
	values, err := ParseDotEnv(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatalf("want no error for a missing file, got %v", err)
	}
	if values != nil {
		t.Fatalf("want nil map for a missing file, got %v", values)
	}
}

func TestParseDotEnvRejectsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("LOG_LEVEL=info\nthis is not an assignment\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if _, err := ParseDotEnv(path); err == nil {
		t.Fatal("want an error for a malformed line")
	} else if !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Fatalf("error should say what was expected, got %v", err)
	}
}

func TestEnvKey(t *testing.T) {
	if got := EnvKey("server.request_timeout"); got != "SERVER_REQUEST_TIMEOUT" {
		t.Fatalf("want SERVER_REQUEST_TIMEOUT, got %q", got)
	}
}
