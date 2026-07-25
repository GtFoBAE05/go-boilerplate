package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// .env.example is the documented list of every setting, so a key that no longer
// exists — or a value that would not validate — has to fail here. Nothing else
// in CI loads it, and a developer copying it to .env is the first to find out
// otherwise.
//
// It is loaded as a real .env: copied into an empty working directory so the
// same code path a developer hits is the one under test.
func TestShippedExampleEnvIsValid(t *testing.T) {
	example, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DotEnvFile), example, 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf(".env.example does not produce a valid config: %v", err)
	}
	if cfg.Service.Name == "" {
		t.Fatal("service.name empty after load")
	}
	// A value only present in .env.example, proving the file was actually read
	// rather than the defaults quietly standing in for it.
	if cfg.Log.Level != "debug" {
		t.Fatalf("want log level from .env.example (debug), got %q", cfg.Log.Level)
	}
}

// Every key in .env.example must be one the app actually reads. A typo
// here is invisible at runtime — the setting is simply ignored forever.
func TestExampleEnvHasNoUnknownKeys(t *testing.T) {
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatalf("open .env.example: %v", err)
	}
	defer func() { _ = f.Close() }()

	known := map[string]bool{}
	v := viper.New()
	setDefaults(v)
	for _, key := range v.AllKeys() {
		known[envKey(key)] = true
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _ := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !known[key] {
			t.Errorf("%s is in .env.example but no such setting exists", key)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

// Defaults alone (no .env present) must be valid, since that is what a
// container setting only what it needs runs on.
func TestDefaultsAreValid(t *testing.T) {
	t.Chdir(t.TempDir())

	if _, err := Load(); err != nil {
		t.Fatalf("defaults are invalid: %v", err)
	}
}

// A real environment variable must beat .env, or a deployment could not
// override a value baked into an image's file.
func TestEnvironmentBeatsDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DotEnvFile),
		[]byte("SERVER_PORT=1111\nLOG_LEVEL=warn\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	t.Setenv("SERVER_PORT", "2222")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 2222 {
		t.Fatalf("environment must win over .env: want port 2222, got %d", cfg.Server.Port)
	}
	// The key the environment did not set still comes from .env.
	if cfg.Log.Level != "warn" {
		t.Fatalf("want log level from .env (warn), got %q", cfg.Log.Level)
	}
}

func TestParseDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DotEnvFile)
	content := "" +
		"# a comment\n" +
		"\n" +
		"export LOG_FORMAT=json\n" +
		`POSTGRES_DSN="host=localhost password='s3 cret'"` + "\n" +
		"REDIS_PASSWORD=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	got, err := parseDotEnv(path)
	if err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}
	want := map[string]string{
		"LOG_FORMAT":     "json",
		"POSTGRES_DSN":   "host=localhost password='s3 cret'",
		"REDIS_PASSWORD": "",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d entries, got %d (%v)", len(want), len(got), got)
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Errorf("%s: want %q, got %q", key, wantValue, got[key])
		}
	}
}

// Parsing must not touch the process environment: that would make Load
// non-idempotent and leak across tests sharing a process.
func TestParseDotEnvDoesNotMutateEnvironment(t *testing.T) {
	const key = "LOG_FORMAT"
	before, hadBefore := os.LookupEnv(key)

	path := filepath.Join(t.TempDir(), DotEnvFile)
	if err := os.WriteFile(path, []byte(key+"=json\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if _, err := parseDotEnv(path); err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}

	after, hadAfter := os.LookupEnv(key)
	if hadBefore != hadAfter || before != after {
		t.Fatalf("parseDotEnv mutated the environment: %s went from (%q,%v) to (%q,%v)",
			key, before, hadBefore, after, hadAfter)
	}
}

// A missing .env is the normal production case, not an error.
func TestParseDotEnvMissingFileIsNotAnError(t *testing.T) {
	values, err := parseDotEnv(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatalf("want no error for a missing file, got %v", err)
	}
	if values != nil {
		t.Fatalf("want nil map for a missing file, got %v", values)
	}
}

func TestParseDotEnvRejectsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), DotEnvFile)
	if err := os.WriteFile(path, []byte("LOG_LEVEL=info\nthis is not an assignment\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if _, err := parseDotEnv(path); err == nil {
		t.Fatal("want an error for a malformed line")
	} else if !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Fatalf("error should say what was expected, got %v", err)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			"request_timeout not below write_timeout",
			func(c *Config) {
				c.Server.WriteTimeout = 10 * time.Second
				c.Server.RequestTimeout = 10 * time.Second
			},
			"request_timeout",
		},
		{
			"shutdown shorter than one request",
			func(c *Config) {
				c.Server.RequestTimeout = 20 * time.Second
				c.Server.ShutdownTimeout = 5 * time.Second
			},
			"shutdown_timeout",
		},
		{"unknown env", func(c *Config) { c.Service.Env = "prod" }, "service.env"},
		{"port out of range", func(c *Config) { c.Server.Port = 70000 }, "server.port"},
		{"pprof port collides", func(c *Config) { c.Pprof = PprofConfig{Enabled: true, Port: c.Server.Port} }, "pprof.port"},
		{"pprof port out of range", func(c *Config) { c.Pprof = PprofConfig{Enabled: true, Port: 70000} }, "pprof.port"},
		{"pprof port unset while enabled", func(c *Config) { c.Pprof = PprofConfig{Enabled: true} }, "pprof.port"},
		{"empty dsn", func(c *Config) { c.Postgres.DSN = "  " }, "postgres.dsn"},
		{"min above max conns", func(c *Config) { c.Postgres.MinConns = 99 }, "min_conns"},
		{"bad exec mode", func(c *Config) { c.Postgres.QueryExecMode = "prepared" }, "query_exec_mode"},
		{"bad redis mode", func(c *Config) { c.Redis.Mode = "maybe" }, "redis.mode"},
		{"bad log level", func(c *Config) { c.Log.Level = "verbose" }, "log.level"},
		{"bad log format", func(c *Config) { c.Log.Format = "xml" }, "log.format"},
		{"sampler arg above 1", func(c *Config) { c.OTel.TracesSamplerArg = 1.5 }, "otel.traces_sampler_arg"},
		{"unknown sampler", func(c *Config) { c.OTel.TracesSampler = "jaeger_remote" }, "otel.traces_sampler"},
		{"unknown exporter", func(c *Config) { c.OTel.MetricsExporter = "prometheus" }, "otel.metrics_exporter"},
		// The pre-OTLP spelling: a bare host:port has no scheme, so nothing
		// decides whether the connection is encrypted.
		{"endpoint without scheme", func(c *Config) { c.OTel.Endpoint = "localhost:4317" }, "scheme"},
		{"zero metric interval", func(c *Config) { c.OTel.MetricExportIntervalMillis = 0 }, "otel.metric_export_interval"},
		{"zero ratelimit requests", func(c *Config) { c.RateLimit.Requests = 0 }, "ratelimit.requests"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)

			err := cfg.validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error mentioning %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// validate must report every problem at once, so one boot surfaces the whole
// list instead of making the operator fix errors one restart at a time.
func TestValidateReportsAllErrors(t *testing.T) {
	cfg := validConfig()
	cfg.Service.Env = "nope"
	cfg.Log.Level = "loud"
	cfg.Server.Port = -1

	err := cfg.validate()
	if err == nil {
		t.Fatal("want errors, got nil")
	}
	for _, want := range []string{"service.env", "log.level", "server.port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("joined error missing %q: %v", want, err)
		}
	}
}

// A disabled subsystem's settings are inert: an invalid pprof port must not
// block boot when pprof is off, the same way tracing.endpoint is only required
// when tracing is enabled. Otherwise the pre-filled default port in .env.example
// would be a landmine for anyone who changes server.port to 6060.
func TestDisabledPprofSettingsAreInert(t *testing.T) {
	cfg := validConfig()
	cfg.Pprof = PprofConfig{Enabled: false, Port: cfg.Server.Port}

	if err := cfg.validate(); err != nil {
		t.Fatalf("disabled pprof must not be validated: %v", err)
	}
}

// OTEL_METRIC_EXPORT_INTERVAL is milliseconds per the OTel spec, not a Go
// duration like every other interval in this config. Getting this wrong is
// silent: "60000" read as nanoseconds would push 16000 times a second.
func TestMetricExportIntervalIsMilliseconds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DotEnvFile),
		[]byte("OTEL_METRIC_EXPORT_INTERVAL=15000\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.OTel.MetricExportInterval(); got != 15*time.Second {
		t.Fatalf("OTEL_METRIC_EXPORT_INTERVAL=15000 must mean 15s, got %s", got)
	}
}

// A disabled signal's settings are inert, so a deployment that exports nothing
// is not blocked by an endpoint it never dials.
func TestDisabledOTelSettingsAreInert(t *testing.T) {
	cfg := validConfig()
	cfg.OTel.SDKDisabled = true
	cfg.OTel.Endpoint = "not a url"
	cfg.OTel.TracesSampler = "nonsense"
	cfg.OTel.MetricExportIntervalMillis = 0

	if err := cfg.validate(); err != nil {
		t.Fatalf("disabled telemetry must not be validated: %v", err)
	}
}

// The production-safe default matters because a deployment ships no file:
// replicas racing to migrate on rollout must be opt-in.
func TestMigrateDefaultsOff(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.Postgres.Migrate {
		t.Fatal("postgres.migrate must default to false")
	}
	if cfg.Pprof.Enabled {
		t.Fatal("pprof.enabled must default to false")
	}
}

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{"url form", "postgres://app:s3cret@localhost:5433/app?sslmode=disable", "postgres://app:xxxxx@localhost:5433/app?sslmode=disable"},
		{"url without password", "postgres://localhost:5433/app", "postgres://localhost:5433/app"},
		{"keyword form", "host=localhost password=s3cret dbname=app", "host=localhost password=xxxxx dbname=app"},
		{"keyword quoted", "host=localhost password='s3 cret' dbname=app", "host=localhost password=xxxxx dbname=app"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactDSN(tt.dsn)
			if got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
			if strings.Contains(got, "s3cret") || strings.Contains(got, "s3 cret") {
				t.Fatalf("password leaked in %q", got)
			}
		})
	}
}

// LogValue is the guard that stops a whole-config log line from leaking
// credentials, so assert the secret is absent from the rendered value.
func TestPostgresLogValueRedactsPassword(t *testing.T) {
	p := PostgresConfig{DSN: "postgres://app:s3cret@localhost:5433/app", QueryExecMode: "exec"}

	if rendered := p.LogValue().String(); strings.Contains(rendered, "s3cret") {
		t.Fatalf("password leaked in log value: %s", rendered)
	}
}

func TestRedisLogValueRedactsPassword(t *testing.T) {
	r := RedisConfig{Mode: RedisModeOptional, Addr: "localhost:6380", Password: "s3cret"}

	if rendered := r.LogValue().String(); strings.Contains(rendered, "s3cret") {
		t.Fatalf("password leaked in log value: %s", rendered)
	}
}

// validConfig mirrors the defaults; each test breaks exactly one field so a
// failure names the rule under test.
func validConfig() *Config {
	return &Config{
		Service: ServiceConfig{Name: "test", Env: EnvProduction},
		Server:  ServerConfig{Port: 8080, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, ShutdownTimeout: 20 * time.Second, RequestTimeout: 20 * time.Second, DrainDelay: 5 * time.Second},
		Postgres: PostgresConfig{
			DSN: "postgres://app:app@localhost:5433/app", MaxConns: 25, MinConns: 2,
			MaxConnLifetime: time.Hour, QueryExecMode: "cache_statement",
		},
		Redis: RedisConfig{Mode: RedisModeOptional, Addr: "localhost:6380"},
		Cache: CacheConfig{DefaultTTL: 5 * time.Minute},
		OTel: OTelConfig{
			TracesExporter: OTelExporterOTLP, MetricsExporter: OTelExporterOTLP,
			Endpoint: "http://localhost:4317", TracesSampler: "parentbased_traceidratio",
			TracesSamplerArg: 1.0, MetricExportIntervalMillis: 60000,
		},
		Log:       LogConfig{Level: "info", Format: "json"},
		RateLimit: RateLimitConfig{Enabled: true, Requests: 40, Window: time.Second},
	}
}
