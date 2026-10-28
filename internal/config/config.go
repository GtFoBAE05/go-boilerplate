// Package config loads and validates this application's settings from the
// environment, with .env as a development convenience. Section types and
// validators live here; internal/platform/config stays generic. .env.example
// is the canonical key list.
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
	Service  ServiceConfig  `mapstructure:"service"`
	Server   ServerConfig   `mapstructure:"server"`
	Pprof    PprofConfig    `mapstructure:"pprof"`
	Postgres PostgresConfig `mapstructure:"postgres"`
	Redis    RedisConfig    `mapstructure:"redis"`
	OTel     OTelConfig     `mapstructure:"otel"`
	Log      LogConfig      `mapstructure:"log"`
}

// DotEnvFile is the optional local overrides file, loaded from the working
// directory. It is git-ignored; .env.example documents every key.
const DotEnvFile = ".env"

// Load builds the API configuration from environment variables, falling back
// to setDefaults. A .env file is layered underneath, so precedence is
// environment > .env > defaults; deployments ship no file.
func Load() (*Config, error) {
	cfg, err := load()
	if err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// LoadMigrate loads only the settings cmd/migrate needs. The API's Redis and
// telemetry settings must not block a migration job.
func LoadMigrate() (*Config, error) {
	cfg, err := load()
	if err != nil {
		return nil, err
	}
	if err := cfg.validateMigrate(); err != nil {
		return nil, fmt.Errorf("invalid migration config: %w", err)
	}
	return cfg, nil
}

func load() (*Config, error) {
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
	return &cfg, nil
}

// validate rejects values the app would otherwise silently misinterpret.
// Section rules live in validate.go; only cross-section checks belong here.
func (c *Config) validate() error {
	errs := []error{
		ValidateService(&c.Service),
		ValidateServer(&c.Server),
		ValidatePprof(&c.Pprof),
		ValidatePostgres(&c.Postgres),
		ValidateRedis(&c.Redis),
		ValidateOTel(&c.OTel),
		ValidateLog(&c.Log),
	}

	// Two listeners on the same port would make one of them fail at bind time
	// with a much less obvious message, and only when pprof is turned on.
	if c.Pprof.Enabled && c.Pprof.Port == c.Server.Port {
		errs = append(errs, fmt.Errorf("pprof.port (%d) must differ from server.port", c.Pprof.Port))
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
	// A production DSN must verify the server certificate; sslmode=disable
	// silently sends credentials and data in plaintext.
	if c.Service.Env == EnvProduction && strings.TrimSpace(c.Postgres.DSN) != "" {
		if err := validatePostgresTLS(c.Postgres.DSN); err != nil {
			errs = append(errs, fmt.Errorf("postgres.dsn: %w", err))
		}
	}

	return errors.Join(errs...)
}

// validateMigrate keeps the migration job independent of the API's Redis and
// telemetry settings. Production requires a dedicated DDL role so the runtime
// role never needs CREATE/ALTER/DROP.
func (c *Config) validateMigrate() error {
	errs := []error{
		ValidateService(&c.Service),
		ValidatePostgresMigration(&c.Postgres),
		ValidateLog(&c.Log),
	}
	if c.Service.Env == EnvProduction {
		if strings.TrimSpace(c.Postgres.MigrationDSN) == "" {
			errs = append(errs, errors.New(
				"postgres.migration_dsn must be set in production; point it at a DDL role, not the runtime DSN"))
		} else if err := validatePostgresTLS(c.Postgres.MigrationDSN); err != nil {
			errs = append(errs, fmt.Errorf("postgres.migration_dsn: %w", err))
		}
	}
	return errors.Join(errs...)
}

// setDefaults registers every key with its production-safe value. Viper's
// AutomaticEnv only resolves registered keys, so an unregistered key is
// invisible even when its variable is set. .env.example opts into dev values.
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
	v.SetDefault("server.trusted_proxy_cidrs", "")
	// Off by default; the port is pre-filled so enabling it needs one variable.
	v.SetDefault("pprof.enabled", false)
	v.SetDefault("pprof.port", 6060)

	// No default DSN: production must set an explicit, TLS-verified connection
	// string. .env.example supplies the local development one.
	v.SetDefault("postgres.dsn", "")
	v.SetDefault("postgres.migration_dsn", "")
	v.SetDefault("postgres.max_conns", 25)
	v.SetDefault("postgres.min_conns", 2)
	v.SetDefault("postgres.max_conn_lifetime", "1h")
	v.SetDefault("postgres.max_conn_lifetime_jitter", "5m")
	v.SetDefault("postgres.statement_timeout", "10s")
	v.SetDefault("postgres.lock_timeout", "5s")
	v.SetDefault("postgres.idle_in_transaction_timeout", "30s")
	v.SetDefault("postgres.migrate", false)
	v.SetDefault("postgres.query_exec_mode", "cache_statement")

	v.SetDefault("redis.mode", string(RedisModeOptional))
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
	// No default endpoint: an enabled exporter must point somewhere explicit,
	// not silently at localhost in production.
	v.SetDefault("otel.exporter_otlp_endpoint", "")
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
}
