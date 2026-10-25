package config

import (
	"log/slog"
	"time"

	platformconfig "github.com/GtFoBAE05/go-boilerplate/internal/platform/config"
)

// ServiceConfig identifies this service in logs and telemetry. It is a section
// so the variables read SERVICE_NAME and SERVICE_ENV, not collision-prone NAME/ENV.
type ServiceConfig struct {
	Name string `mapstructure:"name"`
	Env  string `mapstructure:"env"`
}

// Env values accepted in SERVICE_ENV; they gate logging and telemetry defaults.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// ServerConfig holds the HTTP listener's port and timeout budget.
type ServerConfig struct {
	Port        int           `mapstructure:"port"`
	ReadTimeout time.Duration `mapstructure:"read_timeout"`
	// ReadHeaderTimeout is the tighter slowloris guard; read_timeout covers the
	// whole request. Must not exceed read_timeout, or it is unreachable.
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout   time.Duration `mapstructure:"shutdown_timeout"`
	// RequestTimeout bounds each request's context; it must stay below
	// write_timeout so a 504 can still reach the client.
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	// DrainDelay is how long /readyz reports 503 before shutdown; 0 disables it.
	DrainDelay time.Duration `mapstructure:"drain_delay"`
}

// PprofConfig gates the loopback-only profiling listener. Disabled means no
// socket and no goroutine; metrics are pushed over OTLP, not served here.
type PprofConfig struct {
	// Enabled turns the listener on; off by default is an attack-surface decision.
	Enabled bool `mapstructure:"enabled"`
	// Port is the loopback port to bind when Enabled; ignored otherwise.
	Port int `mapstructure:"port"`
}

// PostgresConfig holds the pgx pool settings and startup-migration flag.
type PostgresConfig struct {
	DSN             string        `mapstructure:"dsn"`
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
	// Migrate runs embedded goose migrations at boot. Off by default so
	// replicas cannot race on rollout; .env.example enables it for local dev.
	Migrate bool `mapstructure:"migrate"`
	// QueryExecMode selects pgx's statement protocol. Behind a transaction-mode
	// pooler (pgbouncer, RDS Proxy, Supabase 6543) use "exec" or
	// "simple_protocol"; cache_statement breaks there.
	QueryExecMode string `mapstructure:"query_exec_mode"`
}

// LogValue redacts the DSN's password so logging a Config (or a
// PostgresConfig) can never leak credentials.
func (p PostgresConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("dsn", platformconfig.RedactDSN(p.DSN)),
		slog.Int64("max_conns", int64(p.MaxConns)),
		slog.Int64("min_conns", int64(p.MinConns)),
		slog.Duration("max_conn_lifetime", p.MaxConnLifetime),
		slog.Bool("migrate", p.Migrate),
		slog.String("query_exec_mode", p.QueryExecMode),
	)
}

// RedisMode controls how the app treats its Redis dependency.
type RedisMode string

const (
	// RedisModeDisabled skips Redis entirely: no connection and no Redis-backed
	// features.
	RedisModeDisabled RedisMode = "disabled"
	// RedisModeOptional degrades gracefully: an unreachable Redis logs a
	// warning and the app runs without the features Redis backs.
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
	// TLSEnabled turns on TLS for the Redis connection. Managed Redis services
	// normally require it; local development normally does not.
	TLSEnabled bool `mapstructure:"tls_enabled"`
	// TLSInsecureSkipVerify disables certificate verification; production rejects
	// it because encryption without identity checks is not safe.
	TLSInsecureSkipVerify bool          `mapstructure:"tls_insecure_skip_verify"`
	DialTimeout           time.Duration `mapstructure:"dial_timeout"`
	// ReadTimeout and WriteTimeout bound one command, keeping a wedged Redis from
	// consuming the whole request budget.
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
	PoolSize     int           `mapstructure:"pool_size"`
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
		slog.Bool("tls_enabled", r.TLSEnabled),
		slog.Bool("tls_insecure_skip_verify", r.TLSInsecureSkipVerify),
		slog.Duration("dial_timeout", r.DialTimeout),
		slog.Duration("read_timeout", r.ReadTimeout),
		slog.Duration("write_timeout", r.WriteTimeout),
		slog.Int("pool_size", r.PoolSize),
	)
}

// OTelConfig controls OTLP export and uses the OpenTelemetry SDK's own variable
// names. OTEL_SERVICE_NAME is the exception: SERVICE_NAME owns the identity
// because logging uses it too. Metric intervals are milliseconds, per spec.
type OTelConfig struct {
	// SDKDisabled turns off both signals; one signal is disabled with
	// TracesExporter/MetricsExporter = "none".
	SDKDisabled bool `mapstructure:"sdk_disabled"`

	// TracesExporter and MetricsExporter accept "otlp" or "none".
	TracesExporter  string `mapstructure:"traces_exporter"`
	MetricsExporter string `mapstructure:"metrics_exporter"`

	// Endpoint is the collector base URL. Its scheme decides transport
	// security, which is why there is no separate insecure flag.
	Endpoint string `mapstructure:"exporter_otlp_endpoint"`
	// TracesEndpoint and MetricsEndpoint override Endpoint for one signal.
	TracesEndpoint  string `mapstructure:"exporter_otlp_traces_endpoint"`
	MetricsEndpoint string `mapstructure:"exporter_otlp_metrics_endpoint"`

	// TracesSampler is a spec sampler name; see telemetry.NewSampler.
	TracesSampler string `mapstructure:"traces_sampler"`
	// TracesSamplerArg is the ratio for the traceidratio samplers.
	TracesSamplerArg float64 `mapstructure:"traces_sampler_arg"`

	// MetricExportIntervalMillis is the push period, in milliseconds.
	MetricExportIntervalMillis int `mapstructure:"metric_export_interval"`
}

// TracesEnabled reports whether spans should be exported.
func (o OTelConfig) TracesEnabled() bool {
	return !o.SDKDisabled && o.TracesExporter == OTelExporterOTLP
}

// MetricsEnabled reports whether metrics should be exported. There is no scrape
// endpoint, so disabling this means no metrics at all.
func (o OTelConfig) MetricsEnabled() bool {
	return !o.SDKDisabled && o.MetricsExporter == OTelExporterOTLP
}

// TraceEndpoint resolves the per-signal override against the base endpoint.
func (o OTelConfig) TraceEndpoint() string {
	if o.TracesEndpoint != "" {
		return o.TracesEndpoint
	}
	return o.Endpoint
}

// MetricEndpoint resolves the per-signal override against the base endpoint.
func (o OTelConfig) MetricEndpoint() string {
	if o.MetricsEndpoint != "" {
		return o.MetricsEndpoint
	}
	return o.Endpoint
}

// MetricExportInterval converts the spec's millisecond integer to a Duration.
func (o OTelConfig) MetricExportInterval() time.Duration {
	return time.Duration(o.MetricExportIntervalMillis) * time.Millisecond
}

// Exporter values accepted in OTEL_TRACES_EXPORTER / OTEL_METRICS_EXPORTER.
const (
	// OTelExporterOTLP pushes over OTLP/gRPC.
	OTelExporterOTLP = "otlp"
	// OTelExporterNone disables the signal.
	OTelExporterNone = "none"
)

// OTelSamplers are the OTEL_TRACES_SAMPLER values this app implements; the
// spec's jaeger_remote and xray are rejected rather than silently ignored.
var OTelSamplers = []string{
	"always_on",
	"always_off",
	"traceidratio",
	"parentbased_always_on",
	"parentbased_always_off",
	"parentbased_traceidratio",
}

// LogConfig sets slog level ("debug".."error") and format ("text" or "json").
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// RateLimitConfig bounds per-client-IP request rate on the API subtree; probes
// are never limited. The counter is shared across replicas when Redis is
// available, so the cap is global rather than per-process.
type RateLimitConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Requests int           `mapstructure:"requests"`
	Window   time.Duration `mapstructure:"window"`
	// AllowProcessLocal acknowledges one budget per process; production otherwise
	// requires Redis, because N local counters silently allow N*requests.
	AllowProcessLocal bool `mapstructure:"allow_process_local"`
}
