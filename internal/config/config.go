// Package config loads and validates application configuration from environment
// variables (e.g. SERVER_PORT=9090), with a local .env file as a development
// convenience and production-safe defaults underneath.
//
// Variable names are unprefixed, which puts them in a namespace shared with
// every other tool in the environment. Two sections are named defensively as a
// result: "service" (so the generic NAME and ENV become SERVICE_NAME and
// SERVICE_ENV) and "tracing" (so we do not squat OTEL_*, which the OpenTelemetry
// SDK reads itself). Check any new key against what an orchestrator or sidecar
// might already set - Kubernetes injects <SERVICE>_PORT and <SERVICE>_SERVICE_HOST
// for every Service in the namespace, so a key spelling out to POSTGRES_PORT or
// REDIS_PORT would be silently overwritten.
//
// There is deliberately one vocabulary. An earlier revision kept a config.yaml
// of dotted keys that production never read, so every knob had two spellings
// and the env names were documented nowhere. .env.example is now the single
// list of available settings, written exactly as a deployment sets them.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root of all application settings, one field per subsystem.
type Config struct {
	Service   ServiceConfig   `mapstructure:"service"`
	Server    ServerConfig    `mapstructure:"server"`
	Pprof     PprofConfig     `mapstructure:"pprof"`
	Postgres  PostgresConfig  `mapstructure:"postgres"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Cache     CacheConfig     `mapstructure:"cache"`
	Tracing   TracingConfig   `mapstructure:"tracing"`
	Log       LogConfig       `mapstructure:"log"`
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
}

// ServiceConfig identifies this service in logs and telemetry.
//
// It is a section rather than two root keys so the variables read SERVICE_NAME
// and SERVICE_ENV. Bare NAME and ENV are the two most collision-prone names in
// an unprefixed environment - generic enough that unrelated tooling sets them.
type ServiceConfig struct {
	Name string `mapstructure:"name"`
	Env  string `mapstructure:"env"`
}

// Env values accepted in SERVICE_ENV; they gate log formatting defaults and are
// stamped on every span and log record.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// ServerConfig holds the HTTP listener's port and timeout budget.
type ServerConfig struct {
	Port            int           `mapstructure:"port"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// RequestTimeout bounds each request's context; slow downstream calls
	// (DB, Redis) abort when it fires and handler.WriteServiceError turns the
	// resulting context error into a 504. Validated to stay below
	// write_timeout so that 504 can still reach the client.
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	// DrainDelay is how long /readyz reports 503 before in-flight requests
	// start draining. It exists for orchestrators that remove a pod from
	// service asynchronously: without the gap, connections keep arriving at a
	// server that has already begun shutting down. 0 disables the wait.
	DrainDelay time.Duration `mapstructure:"drain_delay"`
}

// PprofConfig gates the profiling listener. Disabled means the listener is never
// created: no socket, no goroutine.
//
// It is a separate listener from /metrics, which is served on the public router,
// because the two have opposite exposure requirements: a scraper must reach
// metrics from off-host, while pprof — raw memory plus an easy way to burn CPU —
// must never be routable. One listener cannot be both, so pprof is hardcoded to
// loopback with no host knob to get wrong.
type PprofConfig struct {
	// Enabled turns the listener on. Off by default: this is an attack-surface
	// decision, not a performance one — pprof samples nothing until an endpoint
	// is actually requested.
	Enabled bool `mapstructure:"enabled"`
	// Port is the loopback port to bind when Enabled. Ignored otherwise, so it
	// can stay filled in as documentation of the conventional choice.
	Port int `mapstructure:"port"`
}

// PostgresConfig holds the pgx pool settings and startup-migration flag.
type PostgresConfig struct {
	DSN             string        `mapstructure:"dsn"`
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
	// Migrate runs embedded goose migrations at boot. Defaults to false so the
	// production image is safe without a config file: concurrent replicas all
	// racing to migrate on rollout is not something to opt into by accident.
	// .env.example turns it on for local dev.
	Migrate bool `mapstructure:"migrate"`
	// QueryExecMode selects pgx's statement protocol. Leave "cache_statement"
	// when talking to Postgres directly. Behind a connection pooler in
	// transaction mode (pgbouncer, RDS Proxy, Supabase's 6543 port) server-side
	// prepared statements break, and this must be "simple_protocol" or
	// "exec" — a boilerplate-level footgun worth a knob.
	QueryExecMode string `mapstructure:"query_exec_mode"`
}

// LogValue redacts the DSN's password so logging a Config (or a
// PostgresConfig) can never leak credentials.
func (p PostgresConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("dsn", RedactDSN(p.DSN)),
		slog.Int64("max_conns", int64(p.MaxConns)),
		slog.Int64("min_conns", int64(p.MinConns)),
		slog.Duration("max_conn_lifetime", p.MaxConnLifetime),
		slog.Bool("migrate", p.Migrate),
		slog.String("query_exec_mode", p.QueryExecMode),
	)
}

// keywordPassword matches the password field of a libpq keyword/value DSN
// ("host=db password=secret sslmode=require"), which url.Parse cannot redact.
var keywordPassword = regexp.MustCompile(`(?i)\bpassword\s*=\s*('(?:[^']|'')*'|\S+)`)

// RedactDSN replaces the password in a DSN with "xxxxx", handling both accepted
// pgx forms: URL ("postgres://user:pw@host/db") and libpq keyword/value
// ("host=... password=..."). Unparseable input is reported as redacted rather
// than echoed, so a malformed DSN carrying a secret still never reaches the logs.
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

// RedisMode controls how the app treats its Redis dependency.
type RedisMode string

const (
	// RedisModeDisabled skips Redis entirely: no connection, no caching.
	RedisModeDisabled RedisMode = "disabled"
	// RedisModeOptional degrades gracefully: an unreachable Redis logs a
	// warning and the app runs uncached.
	RedisModeOptional RedisMode = "optional"
	// RedisModeRequired makes an unreachable Redis fatal at startup (fail-fast).
	RedisModeRequired RedisMode = "required"
)

// RedisConfig holds the Redis connection settings; Mode decides how hard the
// app depends on it (see RedisMode).
type RedisConfig struct {
	Mode     RedisMode `mapstructure:"mode"`
	Addr     string    `mapstructure:"addr"`
	Password string    `mapstructure:"password"`
	DB       int       `mapstructure:"db"`
}

// LogValue keeps the Redis password out of logs.
func (r RedisConfig) LogValue() slog.Value {
	pw := ""
	if r.Password != "" {
		pw = "xxxxx"
	}
	return slog.GroupValue(
		slog.String("mode", string(r.Mode)),
		slog.String("addr", r.Addr),
		slog.String("password", pw),
		slog.Int("db", r.DB),
	)
}

// CacheConfig tunes the object cache built on Redis.
type CacheConfig struct {
	DefaultTTL time.Duration `mapstructure:"default_ttl"`
}

// TracingConfig controls trace export: OTLP/gRPC endpoint and sampling.
//
// Named "tracing", not "otel", on purpose: the OpenTelemetry SDK reads its own
// OTEL_* variables (OTEL_SERVICE_NAME, OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_SDK_DISABLED, ...). Defining our settings in that namespace would squat a
// prefix another library actively consumes, so an operator setting the real
// OTEL_EXPORTER_OTLP_ENDPOINT would be silently ignored.
type TracingConfig struct {
	Enabled     bool    `mapstructure:"enabled"`
	Endpoint    string  `mapstructure:"endpoint"`
	Insecure    bool    `mapstructure:"insecure"`
	SampleRatio float64 `mapstructure:"sample_ratio"`
}

// LogConfig sets slog level ("debug".."error") and format ("text" or "json").
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// RateLimitConfig bounds per-client-IP request rate on the API subtree. Probes
// and /metrics are never limited (see server.New).
//
// The limiter is per process: N replicas allow N*Requests per window. Swap in a
// shared store (httprate-redis) before relying on it as a hard global cap.
type RateLimitConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Requests is how many requests one IP may make per Window.
	Requests int `mapstructure:"requests"`
	// Window is the length of the counting window.
	Window time.Duration `mapstructure:"window"`
}

// DotEnvFile is the optional local overrides file, loaded from the working
// directory. It is git-ignored; .env.example documents every key.
const DotEnvFile = ".env"

// Load builds the configuration from environment variables (e.g. POSTGRES_DSN),
// falling back to the defaults in setDefaults.
//
// A .env file in the working directory is loaded into the environment first as
// a local development convenience; real environment variables always win, so
// the precedence is environment > .env > defaults. Deployments set variables
// directly and ship no file.
func Load() (*Config, error) {
	dotEnv, err := parseDotEnv(DotEnvFile)
	if err != nil {
		return nil, err
	}

	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	setDefaults(v)

	// Layer .env in as defaults rather than by setting process environment
	// variables. Viper resolves AutomaticEnv before defaults, so a real
	// environment variable still wins for free — and Load leaves no global
	// state behind, which keeps it idempotent and safe to call from tests.
	for _, key := range v.AllKeys() {
		if value, ok := dotEnv[envKey(key)]; ok {
			v.SetDefault(key, value)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

// validate rejects every value the app would otherwise silently misinterpret.
// A boilerplate is copied far more often than it is read, so an unset or
// fat-fingered override must fail at boot rather than degrade in production.
func (c *Config) validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.Service.Name == "" {
		fail("service.name must not be empty")
	}
	switch c.Service.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		fail("service.env must be one of %s|%s|%s, got %q",
			EnvDevelopment, EnvStaging, EnvProduction, c.Service.Env)
	}

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		fail("server.port must be in 1..65535, got %d", c.Server.Port)
	}
	// Only checked when enabled, mirroring tracing.endpoint: a disabled subsystem's
	// settings are inert and must not block boot.
	if c.Pprof.Enabled {
		if c.Pprof.Port < 1 || c.Pprof.Port > 65535 {
			fail("pprof.port must be in 1..65535 when pprof.enabled, got %d", c.Pprof.Port)
		}
		if c.Pprof.Port == c.Server.Port {
			fail("pprof.port (%d) must differ from server.port", c.Pprof.Port)
		}
	}
	for _, d := range []struct {
		key string
		val time.Duration
	}{
		{"server.read_timeout", c.Server.ReadTimeout},
		{"server.write_timeout", c.Server.WriteTimeout},
		{"server.idle_timeout", c.Server.IdleTimeout},
		{"server.shutdown_timeout", c.Server.ShutdownTimeout},
		{"server.request_timeout", c.Server.RequestTimeout},
	} {
		if d.val <= 0 {
			fail("%s must be > 0, got %s", d.key, d.val)
		}
	}
	if c.Server.DrainDelay < 0 {
		fail("server.drain_delay must not be negative, got %s", c.Server.DrainDelay)
	}
	// The 504 is written by the handler after the request context expires, so
	// the write deadline has to outlast the request deadline or the client
	// gets a dropped connection instead of a status.
	if c.Server.RequestTimeout >= c.Server.WriteTimeout {
		fail("server.request_timeout (%s) must be < server.write_timeout (%s)",
			c.Server.RequestTimeout, c.Server.WriteTimeout)
	}
	// Shutdown must be able to outlast one in-flight request, or graceful
	// shutdown truncates responses that were still within their budget.
	if c.Server.ShutdownTimeout < c.Server.RequestTimeout {
		fail("server.shutdown_timeout (%s) must be >= server.request_timeout (%s)",
			c.Server.ShutdownTimeout, c.Server.RequestTimeout)
	}

	if strings.TrimSpace(c.Postgres.DSN) == "" {
		fail("postgres.dsn must not be empty")
	}
	if c.Postgres.MaxConns < 1 {
		fail("postgres.max_conns must be >= 1, got %d", c.Postgres.MaxConns)
	}
	if c.Postgres.MinConns < 0 {
		fail("postgres.min_conns must not be negative, got %d", c.Postgres.MinConns)
	}
	if c.Postgres.MinConns > c.Postgres.MaxConns {
		fail("postgres.min_conns (%d) must be <= postgres.max_conns (%d)",
			c.Postgres.MinConns, c.Postgres.MaxConns)
	}
	switch c.Postgres.QueryExecMode {
	case "cache_statement", "cache_describe", "describe_exec", "exec", "simple_protocol":
	default:
		fail("postgres.query_exec_mode must be one of cache_statement|cache_describe|describe_exec|exec|simple_protocol, got %q",
			c.Postgres.QueryExecMode)
	}

	switch c.Redis.Mode {
	case RedisModeDisabled:
	case RedisModeOptional, RedisModeRequired:
		if c.Redis.Addr == "" {
			fail("redis.addr must be set when redis.mode is %s", c.Redis.Mode)
		}
	default:
		fail("redis.mode must be one of disabled|optional|required, got %q", c.Redis.Mode)
	}
	if c.Redis.DB < 0 {
		fail("redis.db must not be negative, got %d", c.Redis.DB)
	}
	if c.Cache.DefaultTTL <= 0 {
		fail("cache.default_ttl must be > 0, got %s", c.Cache.DefaultTTL)
	}

	if c.Tracing.Enabled {
		if c.Tracing.Endpoint == "" {
			fail("tracing.endpoint must be set when tracing.enabled")
		}
		if c.Tracing.SampleRatio < 0 || c.Tracing.SampleRatio > 1 {
			fail("tracing.sample_ratio must be in 0.0..1.0, got %v", c.Tracing.SampleRatio)
		}
	}

	// Both are matched case-insensitively downstream; validate the same way so
	// a typo fails here instead of silently falling back to info/json.
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		fail("log.level must be one of debug|info|warn|error, got %q", c.Log.Level)
	}
	switch strings.ToLower(c.Log.Format) {
	case "text", "json":
	default:
		fail("log.format must be text or json, got %q", c.Log.Format)
	}

	if c.RateLimit.Enabled {
		if c.RateLimit.Requests <= 0 {
			fail("ratelimit.requests must be > 0 when ratelimit.enabled, got %d", c.RateLimit.Requests)
		}
		if c.RateLimit.Window <= 0 {
			fail("ratelimit.window must be > 0 when ratelimit.enabled, got %s", c.RateLimit.Window)
		}
	}

	return errors.Join(errs...)
}

// setDefaults registers every key. Registration is mandatory, not just a
// convenience: Viper's AutomaticEnv only resolves keys it already knows, so an
// unregistered key would be invisible even when its variable is set.
//
// These values are the production-safe set, because a deployment ships no file
// and runs on exactly this. .env.example opts local development into the
// convenient ones (debug logging, migrate-on-boot).
func setDefaults(v *viper.Viper) {
	v.SetDefault("service.name", "go-boilerplate")
	v.SetDefault("service.env", EnvDevelopment)

	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", "10s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.idle_timeout", "60s")
	v.SetDefault("server.shutdown_timeout", "20s")
	v.SetDefault("server.request_timeout", "20s")
	v.SetDefault("server.drain_delay", "5s")
	// Off by default; the port is pre-filled so enabling it needs one variable.
	v.SetDefault("pprof.enabled", false)
	v.SetDefault("pprof.port", 6060)

	v.SetDefault("postgres.dsn", "postgres://app:app@localhost:5433/app?sslmode=disable")
	v.SetDefault("postgres.max_conns", 25)
	v.SetDefault("postgres.min_conns", 2)
	v.SetDefault("postgres.max_conn_lifetime", "1h")
	// Defaults are the safe production set, since an image ships without
	// file at all; .env.example opts local development into the convenient ones.
	v.SetDefault("postgres.migrate", false)
	v.SetDefault("postgres.query_exec_mode", "cache_statement")

	v.SetDefault("redis.mode", string(RedisModeOptional))
	v.SetDefault("redis.addr", "localhost:6380")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)

	v.SetDefault("cache.default_ttl", "5m")

	v.SetDefault("tracing.enabled", true)
	v.SetDefault("tracing.endpoint", "localhost:4317")
	v.SetDefault("tracing.insecure", true)
	v.SetDefault("tracing.sample_ratio", 1.0)

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	v.SetDefault("ratelimit.enabled", true)
	v.SetDefault("ratelimit.requests", 40)
	v.SetDefault("ratelimit.window", "1s")
}
