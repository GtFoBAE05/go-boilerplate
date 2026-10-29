// Package config holds generic environment helpers: dotenv parsing and DSN
// redaction. Application sections live in internal/config.
package config

import (
	"net/url"
	"regexp"
	"strings"
)

// keywordPassword matches libpq keyword/value passwords, which url.Parse cannot
// redact. sslpassword is the same kind of secret under a different key.
var keywordPassword = regexp.MustCompile(`(?i)\b(password|sslpassword)\s*=\s*('(?:[^']|'')*'|\S+)`)

// RedactDSN replaces the password in URL and libpq keyword/value DSNs. Unparseable
// input is reported as redacted, never echoed, so a secret cannot reach the logs.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	// Keyword/value form has no scheme; url.Parse would return it untouched
	// and leak the password, so handle it first.
	if !strings.Contains(dsn, "://") {
		return keywordPassword.ReplaceAllString(dsn, "${1}=xxxxx")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "[unparseable dsn redacted]"
	}
	// libpq accepts password and sslpassword as query parameters too. Redacted()
	// only handles userinfo, so redact the query form before rendering.
	query := u.Query()
	changed := false
	for key := range query {
		if strings.EqualFold(key, "password") || strings.EqualFold(key, "sslpassword") {
			query.Set(key, "xxxxx")
			changed = true
		}
	}
	if changed {
		u.RawQuery = query.Encode()
	}
	return u.Redacted()
}
