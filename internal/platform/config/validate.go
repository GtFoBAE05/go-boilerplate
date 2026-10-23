package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
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
	switch c.QueryExecMode {
	case "cache_statement", "cache_describe", "describe_exec", "exec", "simple_protocol":
	default:
		errs = append(errs, fmt.Errorf(
			"postgres.query_exec_mode must be one of cache_statement|cache_describe|describe_exec|exec|simple_protocol, got %q",
			c.QueryExecMode))
	}
	return errors.Join(errs...)
}

// ValidateRedis checks the dependency mode and the connection settings it
// implies. Disabled is the only mode where an empty address is legal.
func ValidateRedis(c *RedisConfig) error {
	var errs []error
	switch c.Mode {
	case RedisModeDisabled:
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
	return errors.Join(errs...)
}

// ValidateCache checks the object-cache TTL. A zero TTL would make every write
// expire immediately, which is a silent way to run uncached.
func ValidateCache(c *CacheConfig) error {
	if c.DefaultTTL <= 0 {
		return fmt.Errorf("cache.default_ttl must be > 0, got %s", c.DefaultTTL)
	}
	return nil
}

// ValidateOTel validates only the signals that are actually exported. A broken
// endpoint must not block boot for a deployment that exports neither.
func ValidateOTel(c *OTelConfig) error {
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

// ValidateRateLimit checks the policy when limiting is on; disabled accepts
// whatever is left in the fields.
func ValidateRateLimit(c *RateLimitConfig) error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if c.Requests <= 0 {
		errs = append(errs, fmt.Errorf("ratelimit.requests must be > 0 when ratelimit.enabled, got %d", c.Requests))
	}
	if c.Window <= 0 {
		errs = append(errs, fmt.Errorf("ratelimit.window must be > 0 when ratelimit.enabled, got %s", c.Window))
	}
	return errors.Join(errs...)
}
