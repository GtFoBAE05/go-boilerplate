package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseDotEnv reads KEY=VALUE lines from path. A missing file returns a nil map
// and no error: deployments set real environment variables and ship no file.
//
// It deliberately does not call os.Setenv. Mutating the process environment
// would make Load non-idempotent and leak between tests in a shared process;
// returning a map lets the caller layer the values underneath the real
// environment instead (see Load).
func parseDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Tolerate "export KEY=VALUE" so the same file can be sourced by a shell.
		line = strings.TrimPrefix(line, "export ")

		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, lineNo, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty key", path, lineNo)
		}

		value = strings.TrimSpace(value)
		// Strip one layer of matching quotes, which is how a value with spaces
		// or a trailing # is written.
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' ||
			value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return values, nil
}

// envKey renders a Viper key as the environment variable that overrides it:
// "server.port" -> "SERVER_PORT".
func envKey(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}
