// Package config loads and validates application configuration via Viper:
// config.yaml as the base, overridable per key with APP_-prefixed environment
// variables (e.g. APP_SERVER_PORT=9090).
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root of all application settings, one field per subsystem.
type Config struct {
	App       AppConfig       `mapstructure:"app"`
	Server    ServerConfig    `mapstructure:"server"`
	Postgres  PostgresConfig  `mapstructure:"postgres"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Cache     CacheConfig     `mapstructure:"cache"`
	Otel      OtelConfig      `mapstructure:"otel"`
	Log       LogConfig       `mapstructure:"log"`
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
}

// AppConfig identifies the service (name, environment) in logs and telemetry.
type AppConfig struct {
	Name string `mapstructure:"name"`
	Env  string `mapstructure:"env"`
}

// ServerConfig holds the HTTP listener's port and timeout budget.
type ServerConfig struct {
	Port            int           `mapstructure:"port"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// RequestTimeout bounds each request's context; slow downstream calls
	// (DB, Redis) abort when it fires. Keep it below write_timeout so the
	// 504 can still be written.
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	// PprofPort binds net/http/pprof to localhost:<port> on a separate,
	// private listener. 0 disables it. Never expose it on the public port.
	PprofPort int `mapstructure:"pprof_port"`
}

// PostgresConfig holds the pgx pool settings and startup-migration flag.
type PostgresConfig struct {
	DSN             string        `mapstructure:"dsn"`
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
	// Migrate runs embedded goose migrations at boot. On for local dev; flip
	// off in production and run goose from CI instead.
	Migrate bool `mapstructure:"migrate"`
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

// CacheConfig tunes the object cache built on Redis.
type CacheConfig struct {
	DefaultTTL time.Duration `mapstructure:"default_ttl"`
}

// OtelConfig controls trace export: OTLP/gRPC endpoint and sampling.
type OtelConfig struct {
	Enabled     bool    `mapstructure:"enabled"`
	Endpoint    string  `mapstructure:"endpoint"`
	Insecure    bool    `mapstructure:"insecure"`
	SampleRatio float64 `mapstructure:"sample_ratio"`
}

// LogConfig sets slog level ("debug".."error") and format ("text" or JSON).
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// RateLimitConfig bounds per-client-IP request rate on the API subtree. Probes
// and /metrics are never limited (see server.New).
type RateLimitConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// RPS is the sustained requests/second per IP; Burst is the max allowed in
	// a single 1s window (the actual httprate limit count).
	RPS   int `mapstructure:"rps"`
	Burst int `mapstructure:"burst"`
}

// Load reads config.yaml (working dir or ./config) and overrides every key
// with APP_-prefixed environment variables, e.g. APP_POSTGRES_DSN.
func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")

	v.SetEnvPrefix("APP")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("read config file: %w", err)
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

func (c *Config) validate() error {
	switch c.Redis.Mode {
	case RedisModeDisabled, RedisModeOptional, RedisModeRequired:
	default:
		return fmt.Errorf("redis.mode must be one of disabled|optional|required, got %q", c.Redis.Mode)
	}
	if c.RateLimit.Enabled {
		if c.RateLimit.RPS <= 0 {
			return fmt.Errorf("ratelimit.rps must be > 0 when ratelimit.enabled, got %d", c.RateLimit.RPS)
		}
		if c.RateLimit.Burst < c.RateLimit.RPS {
			return fmt.Errorf("ratelimit.burst (%d) must be >= ratelimit.rps (%d)", c.RateLimit.Burst, c.RateLimit.RPS)
		}
	}
	return nil
}

// setDefaults registers every key so AutomaticEnv can override it even when
// the key is absent from the config file.
func setDefaults(v *viper.Viper) {
	v.SetDefault("app.name", "go-boilerplate")
	v.SetDefault("app.env", "development")

	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", "10s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.idle_timeout", "60s")
	v.SetDefault("server.shutdown_timeout", "15s")
	v.SetDefault("server.request_timeout", "20s")
	v.SetDefault("server.pprof_port", 6060)

	v.SetDefault("postgres.dsn", "postgres://app:app@localhost:5433/app?sslmode=disable")
	v.SetDefault("postgres.max_conns", 25)
	v.SetDefault("postgres.min_conns", 2)
	v.SetDefault("postgres.max_conn_lifetime", "1h")
	v.SetDefault("postgres.migrate", true)

	v.SetDefault("redis.mode", string(RedisModeOptional))
	v.SetDefault("redis.addr", "localhost:6380")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)

	v.SetDefault("cache.default_ttl", "5m")

	v.SetDefault("otel.enabled", true)
	v.SetDefault("otel.endpoint", "localhost:4317")
	v.SetDefault("otel.insecure", true)
	v.SetDefault("otel.sample_ratio", 1.0)

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	v.SetDefault("ratelimit.enabled", true)
	v.SetDefault("ratelimit.rps", 20)
	v.SetDefault("ratelimit.burst", 40)
}
