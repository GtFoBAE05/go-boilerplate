// Package config holds generic environment helpers: dotenv parsing and DSN
// redaction. Application sections live in internal/config.
package config

import (
	"net/url"
	"regexp"
	"strings"
)

// keywordPassword matches libpq keyword/value passwords, which url.Parse cannot redact.
var keywordPassword = regexp.MustCompile(`(?i)\bpassword\s*=\s*('(?:[^']|'')*'|\S+)`)

// RedactDSN replaces the password in URL and libpq keyword/value DSNs. Unparseable
// input is reported as redacted, never echoed, so a secret cannot reach the logs.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	// Keyword/value form has no scheme; url.Parse would return it untouched
	// and leak the password, so handle it first.
	if !strings.Contains(dsn, "://") {
		return keywordPassword.ReplaceAllString(dsn, "password=xxxxx")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "[unparseable dsn redacted]"
	}
	return u.Redacted()
}
