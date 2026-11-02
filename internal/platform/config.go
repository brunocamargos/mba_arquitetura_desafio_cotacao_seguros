package platform

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port      string
	Partners  []Partner
	Tenants   []string
	Telemetry Telemetry

	Resilience Resilience
}

type Resilience struct {
	BreakerConsecutiveFailures uint32
	BreakerOpenFor             time.Duration
	BreakerHalfOpenRequests    uint32
	CacheTTL                   time.Duration
	RedisAddr                  string
}

type Partner struct {
	Name    string
	BaseURL string
}

type Telemetry struct {
	ServiceName string
	Endpoint    string
	Enabled     bool
}

const defaultEndpoints = "partner-slow=http://localhost:9001," +
	"partner-flaky=http://localhost:9002," +
	"partner-degrading=http://localhost:9003"

const defaultTenants = "corretora-a,corretora-b"

const defaultCollector = "http://localhost:4317"

const defaultServiceName = "quotation-api"

func LoadConfig(env func(string) string) (Config, error) {
	cfg := Config{Port: text(env, "PORT", "8080")}

	var err error
	if cfg.Partners, err = parsePartners(text(env, "PARTNER_ENDPOINTS", defaultEndpoints)); err != nil {
		return Config{}, err
	}
	if cfg.Tenants, err = parseTenants(text(env, "TENANTS", defaultTenants)); err != nil {
		return Config{}, err
	}
	if cfg.Telemetry, err = parseTelemetry(env); err != nil {
		return Config{}, err
	}
	if cfg.Resilience, err = parseResilience(env); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseResilience(env func(string) string) (Resilience, error) {
	values := map[string]int{
		"BREAKER_CONSECUTIVE_FAILURES": 5,
		"BREAKER_OPEN_SECONDS":         10,
		"BREAKER_HALF_OPEN_REQUESTS":   2,
		"CACHE_TTL_SECONDS":            300,
	}
	for key := range values {
		raw := env(key)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return Resilience{}, fmt.Errorf("%s: %q is not a positive integer", key, raw)
		}
		values[key] = value
	}

	return Resilience{
		BreakerConsecutiveFailures: uint32(values["BREAKER_CONSECUTIVE_FAILURES"]),
		BreakerOpenFor:             time.Duration(values["BREAKER_OPEN_SECONDS"]) * time.Second,
		BreakerHalfOpenRequests:    uint32(values["BREAKER_HALF_OPEN_REQUESTS"]),
		CacheTTL:                   time.Duration(values["CACHE_TTL_SECONDS"]) * time.Second,
		RedisAddr:                  text(env, "REDIS_ADDR", "localhost:6379"),
	}, nil
}

func parseTelemetry(env func(string) string) (Telemetry, error) {
	telemetry := Telemetry{
		ServiceName: text(env, "OTEL_SERVICE_NAME", defaultServiceName),
		Endpoint:    strings.TrimSpace(text(env, "OTEL_EXPORTER_OTLP_ENDPOINT", defaultCollector)),
	}

	switch disabled := strings.ToLower(strings.TrimSpace(env("OTEL_SDK_DISABLED"))); disabled {
	case "", "false":
		telemetry.Enabled = true
	case "true":
		return Telemetry{Enabled: false}, nil
	default:
		return Telemetry{}, fmt.Errorf("OTEL_SDK_DISABLED: %q is neither true nor false", disabled)
	}

	if u, err := url.Parse(telemetry.Endpoint); err != nil || u.Scheme == "" || u.Host == "" {
		return Telemetry{}, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT: %q is not an absolute URL", telemetry.Endpoint)
	}
	return telemetry, nil
}

func parsePartners(raw string) ([]Partner, error) {
	var partners []Partner
	seen := map[string]bool{}

	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		name, endpoint, found := strings.Cut(item, "=")
		name, endpoint = strings.TrimSpace(name), strings.TrimSpace(endpoint)
		if !found || name == "" || endpoint == "" {
			return nil, fmt.Errorf("PARTNER_ENDPOINTS: %q is not in the name=url format", item)
		}

		if u, err := url.Parse(endpoint); err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("PARTNER_ENDPOINTS: %q is not an absolute URL", endpoint)
		}
		if seen[name] {
			return nil, fmt.Errorf("PARTNER_ENDPOINTS: partner %q is repeated", name)
		}

		seen[name] = true
		partners = append(partners, Partner{Name: name, BaseURL: strings.TrimRight(endpoint, "/")})
	}

	if len(partners) == 0 {
		return nil, fmt.Errorf("PARTNER_ENDPOINTS: at least one partner is required")
	}
	return partners, nil
}

func parseTenants(raw string) ([]string, error) {
	var tenants []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tenants = append(tenants, t)
		}
	}
	if len(tenants) == 0 {
		return nil, fmt.Errorf("TENANTS: at least one broker is required")
	}
	return tenants, nil
}

func Environment() func(string) string { return os.Getenv }

func text(env func(string) string, key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}
