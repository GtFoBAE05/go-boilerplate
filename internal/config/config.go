// Package config loads and validates this application's configuration from
// environment variables (e.g. SERVER_PORT=9090), with a local .env file as a
// development convenience and production-safe defaults underneath.
//
// The section types and their validators live here, next to the defaults that
// compose them. internal/platform/config is deliberately limited to the
// generic dotenv and redaction helpers: a package under platform/ must stay
// usable by any service, and this application's Redis/OTel/rate-limit sections
// are not.
//
// .env.example is the single list of available settings, written exactly as
// a deployment sets them - there is deliberately one vocabulary, not a
// config.yaml shadowing the env vars.
package config

import (
	"errors"
	"fmt"
	"strings"

	platformconfig "github.com/GtFoBAE05/go-boilerplate/internal/platform/config"

	"github.com/spf13/viper"
)

// Config is the root of all application settings, one field per subsystem.
type Config struct {
	Service   ServiceConfig   `mapstructure:"service"`
	Server    ServerConfig    `mapstructure:"server"`
	Pprof     PprofConfig     `mapstructure:"pprof"`
	Postgres  PostgresConfig  `mapstructure:"postgres"`
	Redis     RedisConfig     `mapstructure:"redis"`
	OTel      OTelConfig      `mapstructure:"otel"`
	Log       LogConfig       `mapstructure:"log"`
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
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
	dotEnv, err := platformconfig.ParseDotEnv(DotEnvFile)
	if err != nil {
		return nil, err
	}

	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	setDefaults(v)

	// Layer .env in as defaults rather than by setting process environment
	// variables. Viper resolves AutomaticEnv before defaults, so a real
	// environment variable still wins for free - and Load leaves no global
	// state behind, which keeps it idempotent and safe to call from tests.
	for _, key := range v.AllKeys() {
		if value, ok := dotEnv[platformconfig.EnvKey(key)]; ok {
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
//
// Each section's rules live with its type in validate.go. Only the checks that
// need two sections at once belong here.
func (c *Config) validate() error {
	errs := []error{
		ValidateService(&c.Service),
		ValidateServer(&c.Server),
		ValidatePprof(&c.Pprof),
		ValidatePostgres(&c.Postgres),
		ValidateRedis(&c.Redis),
		ValidateOTel(&c.OTel),
		ValidateLog(&c.Log),
		ValidateRateLimit(&c.RateLimit),
	}

	// Two listeners on the same port would make one of them fail at bind time
	// with a much less obvious message, and only when pprof is turned on.
	if c.Pprof.Enabled && c.Pprof.Port == c.Server.Port {
		errs = append(errs, fmt.Errorf("pprof.port (%d) must differ from server.port", c.Pprof.Port))
	}
	// Only the limiter is correctness-critical: a shared counter is what makes
	// the cap global. A local limiter may not quietly multiply by the replica
	// count.
	if c.Service.Env == EnvProduction &&
		c.RateLimit.Enabled &&
		!c.RateLimit.AllowProcessLocal &&
		c.Redis.Mode != RedisModeRequired {
		errs = append(errs, fmt.Errorf(
			"production with rate limiting requires redis.mode=required (got %q): "+
				"set REDIS_MODE=required, or RATELIMIT_ALLOW_PROCESS_LOCAL=true to accept one budget per replica",
			c.Redis.Mode))
	}
	// Migrations from every replica at once are a race, not a rollout. CI or a
	// dedicated job owns them in production; the boot-time switch is a local
	// development convenience only.
	if c.Service.Env == EnvProduction && c.Postgres.Migrate {
		errs = append(errs, errors.New(
			"postgres.migrate must be false in production: run migrations from CI or a dedicated job, "+
				"not from every replica at boot"))
	}
	// TLS without verification is worse than no TLS: it encrypts traffic to a
	// server whose identity nobody checked.
	if c.Service.Env == EnvProduction && c.Redis.TLSEnabled && c.Redis.TLSInsecureSkipVerify {
		errs = append(errs, errors.New(
			"redis.tls_insecure_skip_verify must be false in production"))
	}

	return errors.Join(errs...)
}

// setDefaults registers every key with its production-safe value; Viper's
// AutomaticEnv only resolves keys it already knows, so an unregistered key
// would be invisible even when its variable is set.
//
// Production-safe means the value a deployment gets when it ships no file:
// SERVICE_ENV=production, Redis required (the distributed rate limiter must
// not silently become per-process), and migrations off. .env.example opts into
// the convenient development values.
func setDefaults(v *viper.Viper) {
	v.SetDefault("service.name", "go-boilerplate")
	v.SetDefault("service.env", EnvProduction)

	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", "10s")
	v.SetDefault("server.read_header_timeout", "5s")
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
	v.SetDefault("postgres.migrate", false)
	v.SetDefault("postgres.query_exec_mode", "cache_statement")

	v.SetDefault("redis.mode", string(RedisModeRequired))
	v.SetDefault("redis.addr", "localhost:6380")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.tls_enabled", false)
	v.SetDefault("redis.tls_insecure_skip_verify", false)
	v.SetDefault("redis.dial_timeout", "5s")
	v.SetDefault("redis.read_timeout", "3s")
	v.SetDefault("redis.write_timeout", "3s")
	v.SetDefault("redis.pool_size", 10)

	v.SetDefault("otel.sdk_disabled", false)
	v.SetDefault("otel.traces_exporter", OTelExporterOTLP)
	v.SetDefault("otel.metrics_exporter", OTelExporterOTLP)
	v.SetDefault("otel.exporter_otlp_endpoint", "http://localhost:4317")
	// Empty means "inherit the base endpoint"; registered anyway because
	// AutomaticEnv only resolves keys Viper already knows.
	v.SetDefault("otel.exporter_otlp_traces_endpoint", "")
	v.SetDefault("otel.exporter_otlp_metrics_endpoint", "")
	v.SetDefault("otel.traces_sampler", "parentbased_traceidratio")
	v.SetDefault("otel.traces_sampler_arg", 1.0)
	// Milliseconds, per spec - this is the OTel SDK's own default.
	v.SetDefault("otel.metric_export_interval", 60000)

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	v.SetDefault("ratelimit.enabled", true)
	v.SetDefault("ratelimit.requests", 40)
	v.SetDefault("ratelimit.window", "1s")
	v.SetDefault("ratelimit.allow_process_local", false)
}
