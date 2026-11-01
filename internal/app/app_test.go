package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/GtFoBAE05/go-boilerplate/internal/config"
)

func testRedisConfig(mode config.RedisMode) *config.Config {
	return &config.Config{
		Redis: config.RedisConfig{
			Mode:         mode,
			Addr:         "127.0.0.1:1",
			DialTimeout:  50 * time.Millisecond,
			ReadTimeout:  50 * time.Millisecond,
			WriteTimeout: 50 * time.Millisecond,
			PoolSize:     1,
		},
	}
}

func TestSetupRedisDisabled(t *testing.T) {
	client, unavailable, closeClient, err := setupRedis(
		context.Background(), testRedisConfig(config.RedisModeDisabled), slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("disabled Redis: %v", err)
	}
	if client != nil || unavailable != nil {
		t.Fatalf("disabled Redis must return no client or startup error, got client=%v err=%v", client, unavailable)
	}
	closeClient()
}

func TestSetupRedisOptionalKeepsClientForRecovery(t *testing.T) {
	client, unavailable, closeClient, err := setupRedis(
		context.Background(), testRedisConfig(config.RedisModeOptional), slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("optional Redis must not fail startup: %v", err)
	}
	if client == nil {
		t.Fatal("optional Redis must keep the client so it can reconnect")
	}
	if unavailable == nil {
		t.Fatal("optional Redis must report the startup failure to readiness")
	}
	if closeClient == nil {
		t.Fatal("optional Redis must return a close function")
	}
	closeClient()
}

func TestSetupRedisRequiredFailsWithoutClient(t *testing.T) {
	client, unavailable, closeClient, err := setupRedis(
		context.Background(), testRedisConfig(config.RedisModeRequired), slog.New(slog.DiscardHandler),
	)
	if err == nil {
		t.Fatal("required Redis must fail startup when unreachable")
	}
	if client != nil {
		t.Fatal("required Redis must not hand a live client to the caller")
	}
	if unavailable != nil {
		t.Fatalf("required Redis must fail instead of reporting degraded, got %v", unavailable)
	}
	closeClient()
}

func preserveDefaultLogger(t *testing.T) {
	t.Helper()
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
}

func TestRunRejectsUnknownTraceSamplerBeforeStartingInfrastructure(t *testing.T) {
	preserveDefaultLogger(t)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "test", Env: config.EnvDevelopment},
		Log:     config.LogConfig{Level: "info", Format: "text"},
		OTel: config.OTelConfig{
			TracesExporter:   config.OTelExporterOTLP,
			Endpoint:         "http://localhost:4317",
			TracesSampler:    "unknown",
			TracesSamplerArg: 1,
		},
	}

	err := Run(cfg)
	if err == nil || !strings.Contains(err.Error(), "configure trace sampler") {
		t.Fatalf("want trace sampler error, got %v", err)
	}
}

func TestRunReturnsPostgresConnectionError(t *testing.T) {
	preserveDefaultLogger(t)

	cfg := &config.Config{
		Service:  config.ServiceConfig{Name: "test", Env: config.EnvDevelopment},
		Log:      config.LogConfig{Level: "info", Format: "text"},
		OTel:     config.OTelConfig{SDKDisabled: true},
		Postgres: config.PostgresConfig{DSN: "://not-a-dsn"},
	}

	err := Run(cfg)
	if err == nil || !strings.Contains(err.Error(), "connect postgres") {
		t.Fatalf("want postgres connection error, got %v", err)
	}
}
