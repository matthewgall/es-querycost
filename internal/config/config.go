// Package config loads application configuration from defaults, config files,
// environment variables and command-line flags.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"es-querycost/internal/auth"
	"es-querycost/internal/cost"
	"es-querycost/internal/datemath"
	"es-querycost/internal/rules"
	"es-querycost/internal/validate"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Plan holds per-plan limits.
type Plan struct {
	CostLimit float64 `mapstructure:"cost_limit"`
	Window    string  `mapstructure:"window"`
}

// CostModel mirrors cost.Model for configuration files.
type CostModel struct {
	TermCost                  float64            `mapstructure:"term_cost"`
	PhraseCost                float64            `mapstructure:"phrase_cost"`
	WildcardMultiplier        float64            `mapstructure:"wildcard_multiplier"`
	LeadingWildcardCost       float64            `mapstructure:"leading_wildcard_cost"`
	MatchAllCost              float64            `mapstructure:"match_all_cost"`
	RangeCost                 float64            `mapstructure:"range_cost"`
	OpenRangeCost             float64            `mapstructure:"open_range_cost"`
	OrClauseCost              float64            `mapstructure:"or_clause_cost"`
	DepthCost                 float64            `mapstructure:"depth_cost"`
	KeywordWildcardMultiplier float64            `mapstructure:"keyword_wildcard_multiplier"`
	FieldWeights              map[string]float64 `mapstructure:"field_weights"`
	DefaultFieldWeight        float64            `mapstructure:"default_field_weight"`
	DefaultSearchFieldPenalty float64            `mapstructure:"default_search_field_penalty"`
}

// AuthConfig configures request authentication.
type AuthConfig struct {
	// Type is one of: none, apikey, jwt, jwks.
	Type string `mapstructure:"type"`
	// APIKey maps key strings to contexts.
	APIKey map[string]ContextFromConfig `mapstructure:"api_keys"`
	// JWTSecret is the HMAC secret for JWT auth.
	JWTSecret string `mapstructure:"jwt_secret"`
	// JWKSURL is the JWKS endpoint URL for RS256/ECDSA token validation.
	JWKSURL string `mapstructure:"jwks_url"`
	// JWTIssuer, if set, is validated against the "iss" claim for JWT/JWKS auth.
	JWTIssuer string `mapstructure:"jwt_issuer"`
	// JWTAudience, if set, is validated against the "aud" claim for JWT/JWKS auth.
	JWTAudience string `mapstructure:"jwt_audience"`
	// JWTPlanClaim is the claim containing the plan. Defaults to "plan".
	JWTPlanClaim string `mapstructure:"jwt_plan_claim"`
	// JWTUserClaim is the claim containing the user id. Defaults to "sub".
	JWTUserClaim string `mapstructure:"jwt_user_claim"`
}

// ContextFromConfig is the serialisable form of auth.Context.
type ContextFromConfig struct {
	UserID string         `mapstructure:"user_id"`
	Plan   string         `mapstructure:"plan"`
	Extra  map[string]any `mapstructure:"extra"`
}

// BuildValidator creates a validate.Validator from the config.
func (c Config) BuildValidator() (validate.Validator, error) {
	if c.Query.Validator == "elasticsearch" {
		es := c.Elasticsearch
		return validate.NewES(es.URL, es.InsecureSkipVerify, es.CACert, es.Username, es.Password)
	}
	return validate.NoOp{}, nil
}

// BuildAuthenticator creates an auth.Authenticator from the config.
func (c Config) BuildAuthenticator() auth.Authenticator {
	switch c.Auth.Type {
	case "apikey":
		raw := make(map[string]auth.Context, len(c.Auth.APIKey))
		for k, v := range c.Auth.APIKey {
			raw[k] = auth.Context{
				UserID: v.UserID,
				Plan:   v.Plan,
				Extra:  v.Extra,
			}
		}
		return auth.NewAPIKey(raw)
	case "jwt":
		return auth.JWT{
			Secret:    []byte(c.Auth.JWTSecret),
			PlanClaim: c.Auth.JWTPlanClaim,
			UserClaim: c.Auth.JWTUserClaim,
		}
	case "jwks":
		return &auth.JWKS{
			URL:       c.Auth.JWKSURL,
			PlanClaim: c.Auth.JWTPlanClaim,
			UserClaim: c.Auth.JWTUserClaim,
			Issuer:    c.Auth.JWTIssuer,
			Audience:  c.Auth.JWTAudience,
		}
	default:
		return auth.NoOp{}
	}
}

// ElasticsearchConfig holds connection details for the upstream cluster.
type ElasticsearchConfig struct {
	URL                 string `mapstructure:"url"`
	InsecureSkipVerify  bool   `mapstructure:"insecure_skip_verify"`
	CACert              string `mapstructure:"ca_cert"`
	Username            string `mapstructure:"username"`
	Password            string `mapstructure:"password"`
}

// LoggingConfig holds logger settings.
type LoggingConfig struct {
	Level    string `mapstructure:"level"`
	Format   string `mapstructure:"format"`
	Requests bool   `mapstructure:"requests"`
}

// ServerConfig controls how the HTTP server listens and proxies.
type ServerConfig struct {
	ListenAddr     string   `mapstructure:"listen_addr"`
	ProxyTimeout   string   `mapstructure:"proxy_timeout"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

// MetricsConfig controls the Prometheus metrics endpoint.
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Path    string `mapstructure:"path"`
}

// QueryConfig controls query parsing, validation, and date-window behaviour.
type QueryConfig struct {
	Validator         string   `mapstructure:"validator"`
	DateField         string   `mapstructure:"date_field"`
	DateFields        []string `mapstructure:"date_fields"`
	RequireWindow     bool     `mapstructure:"require_window"`
	ForbiddenFeatures []string `mapstructure:"forbidden_features"`
}

// LimitsConfig holds plan/quotas and the cost model.
type LimitsConfig struct {
	Plans     map[string]Plan `mapstructure:"plans"`
	CostModel CostModel       `mapstructure:"cost_model"`
}

// Config is the full configuration loaded from files, env and flags.
type Config struct {
	Server        ServerConfig        `mapstructure:"server"`
	Elasticsearch ElasticsearchConfig `mapstructure:"elasticsearch"`
	Metrics       MetricsConfig       `mapstructure:"metrics"`
	Logging       LoggingConfig       `mapstructure:"logging"`
	Auth          AuthConfig          `mapstructure:"auth"`
	Query         QueryConfig         `mapstructure:"query"`
	Limits        LimitsConfig        `mapstructure:"limits"`
}

// Defaults returns the built-in default configuration.
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			ListenAddr:     "127.0.0.1:8080",
			ProxyTimeout:   "30s",
			TrustedProxies: nil,
		},
		Elasticsearch: ElasticsearchConfig{
			URL: "http://localhost:9200",
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Path:    "/metrics",
		},
		Logging: LoggingConfig{
			Level:    "info",
			Format:   "json",
			Requests: true,
		},
		Auth: AuthConfig{
			Type: "none",
		},
		Query: QueryConfig{
			Validator:         "local",
			DateField:         "@timestamp",
			DateFields:        []string{"@timestamp", "timestamp", "date", "created_at"},
			RequireWindow:     false,
			ForbiddenFeatures: []string{"match_all", "open_range"},
		},
		Limits: LimitsConfig{
			Plans: map[string]Plan{
				"free":       {CostLimit: 50, Window: "14d"},
				"starter":    {CostLimit: 200, Window: "30d"},
				"pro":        {CostLimit: 1000, Window: "60d"},
				"enterprise": {CostLimit: 10000, Window: "365d"},
			},
			CostModel: CostModel{
				TermCost:                  1,
				PhraseCost:                2,
				WildcardMultiplier:        10,
				LeadingWildcardCost:       50,
				MatchAllCost:              100,
				RangeCost:                 5,
				OpenRangeCost:             50,
				OrClauseCost:              3,
				DepthCost:                 1,
				KeywordWildcardMultiplier: 3,
				FieldWeights: map[string]float64{
					"asn": 0.5,
				},
				DefaultFieldWeight:        1,
				DefaultSearchFieldPenalty: 2,
			},
		},
	}
}

// Load loads configuration from defaults, config file, environment variables
// and command-line flags. Config file values may contain $VAR or ${VAR}
// placeholders which are expanded from the process environment.
// It also returns the config file path that was loaded, if any.
func Load() (Config, string, error) {
	cfg, path, err := loadWith(os.Args[1:])
	if err == pflag.ErrHelp {
		os.Exit(0)
	}
	return cfg, path, err
}

func loadWith(args []string) (Config, string, error) {
	cfg := Defaults()

	fs := pflag.NewFlagSet("es-querycost", pflag.ContinueOnError)

	v := viper.New()
	v.SetEnvPrefix("ESQUERY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	defaults := map[string]any{
		"server.listen_addr":                 cfg.Server.ListenAddr,
		"server.proxy_timeout":               cfg.Server.ProxyTimeout,
		"server.trusted_proxies":             cfg.Server.TrustedProxies,
		"elasticsearch.url":                  cfg.Elasticsearch.URL,
		"elasticsearch.insecure_skip_verify": cfg.Elasticsearch.InsecureSkipVerify,
		"elasticsearch.ca_cert":              cfg.Elasticsearch.CACert,
		"elasticsearch.username":             cfg.Elasticsearch.Username,
		"elasticsearch.password":             cfg.Elasticsearch.Password,
		"metrics.enabled":                    cfg.Metrics.Enabled,
		"metrics.path":                       cfg.Metrics.Path,
		"logging.level":                      cfg.Logging.Level,
		"logging.format":                     cfg.Logging.Format,
		"logging.requests":                   cfg.Logging.Requests,
		"query.validator":                    cfg.Query.Validator,
		"query.date_field":                   cfg.Query.DateField,
		"query.date_fields":                  cfg.Query.DateFields,
		"query.require_window":               cfg.Query.RequireWindow,
		"query.forbidden_features":           cfg.Query.ForbiddenFeatures,
		"limits.plans":                       cfg.Limits.Plans,
		"limits.cost_model":                  cfg.Limits.CostModel,
		"auth":                               cfg.Auth,
	}
	for key, val := range defaults {
		v.SetDefault(key, val)
	}

	var configFile string
	fs.StringVarP(&configFile, "config", "c", "", "path to config file")
	fs.String("listen-addr", cfg.Server.ListenAddr, "listen address")
	fs.String("elasticsearch-url", cfg.Elasticsearch.URL, "elasticsearch base url")
	if err := fs.Parse(args); err != nil {
		return cfg, "", fmt.Errorf("parse flags: %w", err)
	}

	if err := v.BindPFlag("server.listen_addr", fs.Lookup("listen-addr")); err != nil {
		return cfg, "", fmt.Errorf("bind listen-addr flag: %w", err)
	}
	if err := v.BindPFlag("elasticsearch.url", fs.Lookup("elasticsearch-url")); err != nil {
		return cfg, "", fmt.Errorf("bind elasticsearch-url flag: %w", err)
	}

	if configFile != "" {
		v.SetConfigFile(configFile)
	} else {
		v.SetConfigName("es-querycost")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/es-querycost/")
	}

	if err := readConfigWithEnvExpansion(v); err != nil {
		return cfg, "", fmt.Errorf("read config: %w", err)
	}

	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, "", fmt.Errorf("unmarshal config: %w", err)
	}

	if err := validateTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return cfg, "", fmt.Errorf("trusted_proxies: %w", err)
	}
	if err := validateAuth(cfg.Auth); err != nil {
		return cfg, "", fmt.Errorf("auth: %w", err)
	}

	return cfg, v.ConfigFileUsed(), nil
}

func validateAuth(ac AuthConfig) error {
	switch ac.Type {
	case "jwks":
		if ac.JWKSURL == "" {
			return fmt.Errorf("jwks_url is required")
		}
		u, err := url.Parse(ac.JWKSURL)
		if err != nil {
			return fmt.Errorf("invalid jwks_url: %w", err)
		}
		if u.Scheme != "https" {
			return fmt.Errorf("jwks_url must use https")
		}
		if u.Host == "" {
			return fmt.Errorf("jwks_url must have a host")
		}
	case "apikey":
		if len(ac.APIKey) == 0 {
			return fmt.Errorf("api_keys is required")
		}
	case "jwt":
		if ac.JWTSecret == "" {
			return fmt.Errorf("jwt_secret is required")
		}
	case "none", "":
		return nil
	default:
		return fmt.Errorf("unsupported auth type %q", ac.Type)
	}
	return nil
}

func validateTrustedProxies(cidrs []string) error {
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			return fmt.Errorf("empty trusted_proxy entry")
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("invalid cidr %q: %w", c, err)
		}
	}
	return nil
}

func loadFromReader(r *strings.Reader) (Config, error) {
	v := viper.New()
	cfg := Defaults()
	v.SetConfigType("yaml")
	v.SetEnvPrefix("ESQUERY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	defaults := map[string]any{
		"server.listen_addr":                 cfg.Server.ListenAddr,
		"server.proxy_timeout":               cfg.Server.ProxyTimeout,
		"server.trusted_proxies":             cfg.Server.TrustedProxies,
		"elasticsearch.url":                  cfg.Elasticsearch.URL,
		"elasticsearch.insecure_skip_verify": cfg.Elasticsearch.InsecureSkipVerify,
		"elasticsearch.ca_cert":              cfg.Elasticsearch.CACert,
		"elasticsearch.username":             cfg.Elasticsearch.Username,
		"elasticsearch.password":             cfg.Elasticsearch.Password,
		"metrics.enabled":                    cfg.Metrics.Enabled,
		"metrics.path":                       cfg.Metrics.Path,
		"logging.level":                      cfg.Logging.Level,
		"logging.format":                     cfg.Logging.Format,
		"logging.requests":                   cfg.Logging.Requests,
		"query.validator":                    cfg.Query.Validator,
		"query.date_field":                   cfg.Query.DateField,
		"query.date_fields":                  cfg.Query.DateFields,
		"query.require_window":               cfg.Query.RequireWindow,
		"query.forbidden_features":           cfg.Query.ForbiddenFeatures,
		"limits.plans":                       cfg.Limits.Plans,
		"limits.cost_model":                  cfg.Limits.CostModel,
		"auth":                               cfg.Auth,
	}
	for k, val := range defaults {
		v.SetDefault(k, val)
	}

	if err := v.ReadConfig(r); err != nil {
		return cfg, err
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func readConfigWithEnvExpansion(v *viper.Viper) error {
	var path string
	if v.ConfigFileUsed() != "" {
		path = v.ConfigFileUsed()
	} else {
		if err := v.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				return nil
			}
			return err
		}
		path = v.ConfigFileUsed()
		if path == "" {
			return nil
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	expanded := os.Expand(string(data), func(key string) string {
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return ""
	})

	return v.ReadConfig(strings.NewReader(expanded))
}

// BuildCostModel converts the config struct to the runtime cost model.
func (c Config) BuildCostModel() cost.Model {
	m := c.Limits.CostModel
	return cost.Model{
		TermCost:                  m.TermCost,
		PhraseCost:                m.PhraseCost,
		WildcardMultiplier:        m.WildcardMultiplier,
		LeadingWildcardCost:       m.LeadingWildcardCost,
		MatchAllCost:              m.MatchAllCost,
		RangeCost:                 m.RangeCost,
		OpenRangeCost:             m.OpenRangeCost,
		OrClauseCost:              m.OrClauseCost,
		DepthCost:                 m.DepthCost,
		KeywordWildcardMultiplier: m.KeywordWildcardMultiplier,
		FieldWeights:              m.FieldWeights,
		DefaultFieldWeight:        m.DefaultFieldWeight,
		DefaultSearchFieldPenalty: m.DefaultSearchFieldPenalty,
	}
}

// BuildEngine builds the rule engine from the loaded configuration.
func (c Config) BuildEngine() *rules.Engine {
	windows := make(map[string]time.Duration, len(c.Limits.Plans))
	limits := make(map[string]float64, len(c.Limits.Plans))
	for plan, p := range c.Limits.Plans {
		limits[plan] = p.CostLimit
		if d, ok := datemath.ParseDurationHuman(p.Window); ok {
			windows[plan] = d
		}
	}

	return &rules.Engine{
		Rules: []rules.Rule{
			rules.ForbiddenFeature{Forbidden: c.Query.ForbiddenFeatures},
			rules.PlanLimit{
				Limits:       limits,
				DefaultLimit: 100,
			},
			rules.QueryWindow{
				DateFields:    c.Query.DateFields,
				Windows:       windows,
				DefaultWindow: 30 * 24 * time.Hour,
				RequireWindow: c.Query.RequireWindow,
			},
		},
	}
}

// PlanWindow returns the configured time window string for a plan.
func (c Config) PlanWindow(plan string) string {
	if p, ok := c.Limits.Plans[plan]; ok && p.Window != "" {
		return p.Window
	}
	return "30d"
}
