package config

import (
	"log/slog"
	"time"

	platformconfig "github.com/GtFoBAE05/go-boilerplate/internal/platform/config"
)

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
	Port        int           `mapstructure:"port"`
	ReadTimeout time.Duration `mapstructure:"read_timeout"`
	// ReadHeaderTimeout bounds how long a client may take to send the request
	// line and headers. read_timeout covers that too, but only as part of the
	// whole-request budget: this is the tighter limit that stops a slowloris
	// from holding a connection open cheaply. Must not exceed read_timeout,
	// or it is unreachable.
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout   time.Duration `mapstructure:"shutdown_timeout"`
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
// It is a listener of its own, bound to loopback, because pprof - raw memory
// plus an easy way to burn CPU - must never be routable from off-host. Metrics
// do not share it: they are pushed over OTLP and served by the collector, so
// there is no scrape endpoint on either listener.
type PprofConfig struct {
	// Enabled turns the listener on. Off by default: this is an attack-surface
	// decision, not a performance one - pprof samples nothing until an endpoint
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
	// "exec" - a boilerplate-level footgun worth a knob.
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
	// TLSInsecureSkipVerify disables certificate verification. It is rejected
	// in production: an unverified connection is not worth the convenience of
	// skipping certificate validation.
	TLSInsecureSkipVerify bool `mapstructure:"tls_insecure_skip_verify"`
	// DialTimeout bounds connection establishment.
	DialTimeout time.Duration `mapstructure:"dial_timeout"`
	// ReadTimeout and WriteTimeout bound a single command, not the whole
	// request. They keep a wedged Redis from consuming the request budget.
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
	// PoolSize is the maximum number of socket connections.
	PoolSize int `mapstructure:"pool_size"`
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

// OTelConfig controls OTLP export of traces and metrics.
//
// The keys spell out to the OpenTelemetry SDK's own variable names on purpose.
// The SDK reads those names from the real environment itself, so matching its
// spelling and meaning is what keeps an operator's OTEL_EXPORTER_OTLP_ENDPOINT
// from being silently ignored. Two deliberate deviations:
//   - OTEL_SERVICE_NAME is not read; SERVICE_NAME owns the identity because the
//     logger uses it too.
//   - OTEL_METRIC_EXPORT_INTERVAL is milliseconds, per spec, unlike every other
//     interval here.
//
// Unregistered OTEL_* still reach the exporter from the real environment
// (_HEADERS, _TIMEOUT, _COMPRESSION) - but not from .env, which is parsed into
// Viper rather than exported into the process.
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

// OTelSamplers are the OTEL_TRACES_SAMPLER values this app implements. The
// spec's jaeger_remote and xray are rejected by validation rather than ignored:
// a sampler that silently is not the one you asked for is how an incident ends
// up with no trace to look at.
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

// RateLimitConfig bounds per-client-IP request rate on the API subtree. Probes
// are never limited (see server.New).
//
// Requests and Window describe the policy, not its storage: the counter is
// shared across replicas when Redis is available, so the cap is global rather
// than one budget per process.
type RateLimitConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Requests is how many requests one IP may make per Window.
	Requests int `mapstructure:"requests"`
	// Window is the length of the counting window.
	Window time.Duration `mapstructure:"window"`
	// AllowProcessLocal acknowledges that this deployment accepts one budget
	// per process. In production the limiter is otherwise required to share a
	// counter through Redis: N replicas with local counters allow N*requests,
	// which is a silent multiplication, not a degraded mode.
	AllowProcessLocal bool `mapstructure:"allow_process_local"`
}
