package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ValidateService rejects a config the app would otherwise run with silently.
func ValidateService(c *ServiceConfig) error {
	var errs []error
	if c.Name == "" {
		errs = append(errs, errors.New("service.name must not be empty"))
	}
	switch c.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("service.env must be one of %s|%s|%s, got %q",
			EnvDevelopment, EnvStaging, EnvProduction, c.Env))
	}
	return errors.Join(errs...)
}

// ValidateServer checks the timeout budget as a whole, not each field alone:
// the relationships are what make a 504 reachable and shutdown graceful.
func ValidateServer(c *ServerConfig) error {
	var errs []error
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port must be in 1..65535, got %d", c.Port))
	}
	for _, d := range []struct {
		key string
		val time.Duration
	}{
		{"server.read_timeout", c.ReadTimeout},
		{"server.read_header_timeout", c.ReadHeaderTimeout},
		{"server.write_timeout", c.WriteTimeout},
		{"server.idle_timeout", c.IdleTimeout},
		{"server.shutdown_timeout", c.ShutdownTimeout},
		{"server.request_timeout", c.RequestTimeout},
	} {
		if d.val <= 0 {
			errs = append(errs, fmt.Errorf("%s must be > 0, got %s", d.key, d.val))
		}
	}
	if c.DrainDelay < 0 {
		errs = append(errs, fmt.Errorf("server.drain_delay must not be negative, got %s", c.DrainDelay))
	}
	for _, cidr := range strings.Split(c.TrustedProxyCIDRs, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			errs = append(errs, fmt.Errorf("server.trusted_proxy_cidrs contains invalid CIDR %q: %w", cidr, err))
		}
	}
	// A header deadline above the whole-request deadline can never fire, which
	// looks configured but leaves slowloris protection to the wider timeout.
	if c.ReadHeaderTimeout > c.ReadTimeout {
		errs = append(errs, fmt.Errorf("server.read_header_timeout (%s) must be <= server.read_timeout (%s)",
			c.ReadHeaderTimeout, c.ReadTimeout))
	}
	// The 504 is written by the handler after the request context expires, so
	// the write deadline has to outlast the request deadline or the client
	// gets a dropped connection instead of a status.
	if c.RequestTimeout >= c.WriteTimeout {
		errs = append(errs, fmt.Errorf("server.request_timeout (%s) must be < server.write_timeout (%s)",
			c.RequestTimeout, c.WriteTimeout))
	}
	// Shutdown must be able to outlast one in-flight request, or graceful
	// shutdown truncates responses that were still within their budget.
	if c.ShutdownTimeout < c.RequestTimeout {
		errs = append(errs, fmt.Errorf("server.shutdown_timeout (%s) must be >= server.request_timeout (%s)",
			c.ShutdownTimeout, c.RequestTimeout))
	}
	return errors.Join(errs...)
}

// ValidatePprof checks the private listener when it is enabled. Its relation to
// server.port is a cross-section check and lives in the caller.
func ValidatePprof(c *PprofConfig) error {
	if !c.Enabled {
		return nil
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("pprof.port must be in 1..65535 when pprof.enabled, got %d", c.Port)
	}
	return nil
}

// ValidatePostgres checks the pool settings and the statement protocol.
func ValidatePostgres(c *PostgresConfig) error {
	var errs []error
	if strings.TrimSpace(c.DSN) == "" {
		errs = append(errs, errors.New("postgres.dsn must not be empty"))
	}
	if c.MaxConns < 1 {
		errs = append(errs, fmt.Errorf("postgres.max_conns must be >= 1, got %d", c.MaxConns))
	}
	if c.MinConns < 0 {
		errs = append(errs, fmt.Errorf("postgres.min_conns must not be negative, got %d", c.MinConns))
	}
	if c.MinConns > c.MaxConns {
		errs = append(errs, fmt.Errorf("postgres.min_conns (%d) must be <= postgres.max_conns (%d)",
			c.MinConns, c.MaxConns))
	}
	for _, d := range []struct {
		key string
		val time.Duration
	}{
		{"postgres.statement_timeout", c.StatementTimeout},
		{"postgres.lock_timeout", c.LockTimeout},
		{"postgres.idle_in_transaction_timeout", c.IdleInTransactionTimeout},
	} {
		if d.val < time.Millisecond {
			errs = append(errs, fmt.Errorf("%s must be at least 1ms, got %s", d.key, d.val))
		}
	}
	if c.MaxConnLifetime < 0 {
		errs = append(errs, fmt.Errorf("postgres.max_conn_lifetime must not be negative, got %s",
			c.MaxConnLifetime))
	}
	if c.MaxConnLifetimeJitter < 0 {
		errs = append(errs, fmt.Errorf("postgres.max_conn_lifetime_jitter must not be negative, got %s",
			c.MaxConnLifetimeJitter))
	}
	if c.MaxConnLifetime > 0 && c.MaxConnLifetimeJitter > c.MaxConnLifetime {
		errs = append(errs, fmt.Errorf(
			"postgres.max_conn_lifetime_jitter (%s) must be <= postgres.max_conn_lifetime (%s)",
			c.MaxConnLifetimeJitter, c.MaxConnLifetime))
	}
	switch c.QueryExecMode {
	case "cache_statement", "cache_describe", "describe_exec", "exec", "simple_protocol":
	default:
		errs = append(errs, fmt.Errorf(
			"postgres.query_exec_mode must be one of cache_statement|cache_describe|describe_exec|exec|simple_protocol, got %q",
			c.QueryExecMode))
	}
	return errors.Join(errs...)
}

// ValidatePostgresMigration accepts either the runtime DSN or a dedicated
// migration DSN, so a production migration job never needs the API's role.
func ValidatePostgresMigration(c *PostgresConfig) error {
	if strings.TrimSpace(c.DSN) == "" && strings.TrimSpace(c.MigrationDSN) == "" {
		return errors.New("postgres.migration_dsn or postgres.dsn must be set")
	}
	copy := *c
	if strings.TrimSpace(copy.DSN) == "" {
		copy.DSN = copy.MigrationDSN
	}
	return ValidatePostgres(&copy)
}

// validatePostgresTLS requires certificate verification, not merely encryption.
// sslmode=require encrypts but does not authenticate the server; prefer, allow,
// and disable are weaker still.
func validatePostgresTLS(dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse postgres dsn: %w", err)
	}
	if cfg.TLSConfig == nil {
		return errors.New("must enable TLS in production (use sslmode=verify-full)")
	}
	if cfg.TLSConfig.InsecureSkipVerify && cfg.TLSConfig.VerifyPeerCertificate == nil {
		return errors.New("must verify the server certificate in production (use sslmode=verify-ca or verify-full)")
	}
	return nil
}

// ValidateRedis checks the dependency mode and the connection settings it
// implies. Disabled is the only mode where an empty address is legal.
func ValidateRedis(c *RedisConfig) error {
	var errs []error
	switch c.Mode {
	case RedisModeDisabled:
		// Disabled means the app never dials; the remaining fields are
		// intentionally ignored so a deployment can set only REDIS_MODE.
		return nil
	case RedisModeOptional, RedisModeRequired:
		if c.Addr == "" {
			errs = append(errs, fmt.Errorf("redis.addr must be set when redis.mode is %s", c.Mode))
		}
	default:
		errs = append(errs, fmt.Errorf("redis.mode must be one of disabled|optional|required, got %q", c.Mode))
	}
	if c.DB < 0 {
		errs = append(errs, fmt.Errorf("redis.db must not be negative, got %d", c.DB))
	}
	if c.TLSInsecureSkipVerify && !c.TLSEnabled {
		errs = append(errs, errors.New("redis.tls_insecure_skip_verify requires redis.tls_enabled=true"))
	}
	if c.DialTimeout <= 0 {
		errs = append(errs, fmt.Errorf("redis.dial_timeout must be > 0, got %s", c.DialTimeout))
	}
	if c.ReadTimeout <= 0 {
		errs = append(errs, fmt.Errorf("redis.read_timeout must be > 0, got %s", c.ReadTimeout))
	}
	if c.WriteTimeout <= 0 {
		errs = append(errs, fmt.Errorf("redis.write_timeout must be > 0, got %s", c.WriteTimeout))
	}
	if c.PoolSize < 1 {
		errs = append(errs, fmt.Errorf("redis.pool_size must be >= 1, got %d", c.PoolSize))
	}
	return errors.Join(errs...)
}

// ValidateOTel validates only the signals that are actually exported. A broken
// endpoint must not block boot for a deployment that exports neither.
func ValidateOTel(c *OTelConfig) error {
	if c.SDKDisabled {
		// With the SDK disabled nothing is exported, so an exporter or
		// endpoint left over from a previous deployment must not block boot.
		return nil
	}
	var errs []error
	for _, e := range []struct {
		key   string
		value string
	}{
		{"otel.traces_exporter", c.TracesExporter},
		{"otel.metrics_exporter", c.MetricsExporter},
	} {
		if e.value != OTelExporterOTLP && e.value != OTelExporterNone {
			errs = append(errs, fmt.Errorf("%s must be %s or %s, got %q",
				e.key, OTelExporterOTLP, OTelExporterNone, e.value))
		}
	}
	for _, e := range []struct {
		key      string
		endpoint string
		enabled  bool
	}{
		{"otel.exporter_otlp_traces_endpoint", c.TraceEndpoint(), c.TracesEnabled()},
		{"otel.exporter_otlp_metrics_endpoint", c.MetricEndpoint(), c.MetricsEnabled()},
	} {
		if !e.enabled {
			continue
		}
		if err := ValidateOTLPEndpoint(e.endpoint); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.key, err))
		}
	}
	if c.TracesEnabled() {
		if !slices.Contains(OTelSamplers, c.TracesSampler) {
			errs = append(errs, fmt.Errorf("otel.traces_sampler must be one of %s, got %q",
				strings.Join(OTelSamplers, "|"), c.TracesSampler))
		}
		if c.TracesSamplerArg < 0 || c.TracesSamplerArg > 1 {
			errs = append(errs, fmt.Errorf("otel.traces_sampler_arg must be in 0.0..1.0, got %v",
				c.TracesSamplerArg))
		}
	}
	if c.MetricsEnabled() && c.MetricExportIntervalMillis <= 0 {
		errs = append(errs, fmt.Errorf("otel.metric_export_interval must be > 0 milliseconds, got %d",
			c.MetricExportIntervalMillis))
	}
	return errors.Join(errs...)
}

// ValidateOTLPEndpoint enforces the spec's URL form. The scheme selects TLS, so
// a bare "host:4317" - what everyone types first - has no defined transport
// security and is rejected with the fix spelled out.
func ValidateOTLPEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("must be set while the signal is exported")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("must be a URL, got %q", endpoint)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf(
			"must include an http:// or https:// scheme, which selects transport security; got %q (try %q)",
			endpoint, "http://"+endpoint)
	}
	if u.Host == "" {
		return fmt.Errorf("must include a host, got %q", endpoint)
	}
	return nil
}

// ValidateLog matches case-insensitively, the same way the logger does, so a
// typo fails here instead of silently falling back to info/json.
func ValidateLog(c *LogConfig) error {
	var errs []error
	switch strings.ToLower(c.Level) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level must be one of debug|info|warn|error, got %q", c.Level))
	}
	switch strings.ToLower(c.Format) {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("log.format must be text or json, got %q", c.Format))
	}
	return errors.Join(errs...)
}
