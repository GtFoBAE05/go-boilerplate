package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOptionsSetRuntimeSafetyKnobs(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://app:app@localhost:5433/app")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, opt := range []Option{
		MaxConnLifetimeJitter(5 * time.Minute),
		StatementTimeout(10 * time.Second),
		LockTimeout(5 * time.Second),
		IdleInTransactionTimeout(30 * time.Second),
		ApplicationName("api"),
	} {
		opt(cfg)
	}

	want := map[string]string{
		"statement_timeout":                   "10000",
		"lock_timeout":                        "5000",
		"idle_in_transaction_session_timeout": "30000",
		"application_name":                    "api",
	}
	for key, value := range want {
		if got := cfg.ConnConfig.RuntimeParams[key]; got != value {
			t.Errorf("%s: want %q, got %q", key, value, got)
		}
	}
	if cfg.MaxConnLifetimeJitter != 5*time.Minute {
		t.Fatalf("want jitter 5m, got %s", cfg.MaxConnLifetimeJitter)
	}
}
