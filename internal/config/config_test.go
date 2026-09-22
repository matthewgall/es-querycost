package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.ListenAddr != ":8080" {
		t.Errorf("unexpected listen addr: %s", cfg.ListenAddr)
	}
	if cfg.ElasticsearchURL != "http://localhost:9200" {
		t.Errorf("unexpected es url: %s", cfg.ElasticsearchURL)
	}
	if cfg.RequireWindow {
		t.Error("expected require_window to be false by default")
	}
	if len(cfg.Plans) != 4 {
		t.Errorf("expected 4 default plans, got %d", len(cfg.Plans))
	}
}

func TestEnvExpansionInConfigFile(t *testing.T) {
	os.Setenv("TEST_ES_URL", "http://es-from-env.example.com")
	defer os.Unsetenv("TEST_ES_URL")

	dir := t.TempDir()
	path := filepath.Join(dir, "es-querycost.yaml")
	content := "elasticsearch_url: $TEST_ES_URL\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	v := viper.New()
	v.SetConfigFile(path)
	if err := readConfigWithEnvExpansion(v); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if got := v.GetString("elasticsearch_url"); got != "http://es-from-env.example.com" {
		t.Errorf("expected env substitution, got %s", got)
	}
}

func TestLoadWithConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "es-querycost.yaml")
	content := `
listen_addr: ":9090"
elasticsearch_url: "http://custom.example.com:9200"
require_window: true
plans:
  free:
    cost_limit: 10
    window: "7d"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := loadWith([]string{"--config", path})
	if err != nil {
		t.Fatalf("loadWith: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("listen addr: got %s, want :9090", cfg.ListenAddr)
	}
	if cfg.ElasticsearchURL != "http://custom.example.com:9200" {
		t.Errorf("es url: got %s", cfg.ElasticsearchURL)
	}
	if !cfg.RequireWindow {
		t.Error("expected require_window true")
	}
	if cfg.Plans["free"].CostLimit != 10 {
		t.Errorf("free cost limit: got %f, want 10", cfg.Plans["free"].CostLimit)
	}
	if cfg.Plans["free"].Window != "7d" {
		t.Errorf("free window: got %s, want 7d", cfg.Plans["free"].Window)
	}
}

func TestLoadWithFlagOverridesEnv(t *testing.T) {
	t.Setenv("ESQUERY_LISTEN_ADDR", ":7000")

	cfg, _, err := loadWith([]string{"--listen-addr", ":8000"})
	if err != nil {
		t.Fatalf("loadWith: %v", err)
	}
	if cfg.ListenAddr != ":8000" {
		t.Errorf("flag should override env, got %s", cfg.ListenAddr)
	}
}

func TestEngineFromConfig(t *testing.T) {
	cfg := Defaults()
	engine := cfg.BuildEngine()
	if len(engine.Rules) == 0 {
		t.Error("expected rules from config")
	}
}

func TestLoadWithCustomPlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "es-querycost.yaml")
	content := `
plans:
  startup:
    cost_limit: 75
    window: "21d"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, path, err := loadWith([]string{"--config", path})
	if err != nil {
		t.Fatalf("loadWith: %v", err)
	}
	if path == "" {
		t.Error("expected config file path to be returned")
	}
	if _, ok := cfg.Plans["startup"]; !ok {
		t.Fatal("expected startup plan from config")
	}
	if cfg.Plans["startup"].CostLimit != 75 {
		t.Errorf("cost limit: got %f, want 75", cfg.Plans["startup"].CostLimit)
	}

	engine := cfg.BuildEngine()
	if engine == nil || len(engine.Rules) == 0 {
		t.Fatal("expected engine built from config")
	}
}

func TestWatchReloadsConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "es-querycost.yaml")
	content := `
plans:
  free:
    cost_limit: 10
    window: "7d"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var mu sync.Mutex
	var reloaded Config
	received := make(chan struct{}, 1)
	cancel := Watch(path, func(cfg Config) {
		mu.Lock()
		reloaded = cfg
		mu.Unlock()
		received <- struct{}{}
	})
	defer cancel()

	// Give the watcher a moment to register before mutating the file.
	time.Sleep(100 * time.Millisecond)

	updated := `
plans:
  free:
    cost_limit: 99
    window: "7d"
`
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		t.Fatalf("update config: %v", err)
	}

	select {
	case <-received:
		mu.Lock()
		limit := reloaded.Plans["free"].CostLimit
		mu.Unlock()
		if limit != 99 {
			t.Errorf("expected reloaded limit 99, got %f", limit)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for config reload")
	}
}

func TestBuilders(t *testing.T) {
	cfg := Defaults()
	if cfg.BuildValidator() == nil {
		t.Error("BuildValidator returned nil")
	}
	if cfg.BuildAuthenticator() == nil {
		t.Error("BuildAuthenticator returned nil")
	}
	if cfg.BuildCostModel().TermCost == 0 {
		t.Error("BuildCostModel produced zero term cost")
	}
	if win := cfg.PlanWindow("free"); win == "" {
		t.Error("expected free plan window")
	}
	if win := cfg.PlanWindow("unknown"); win == "" {
		t.Error("expected default window for unknown plan")
	}
}
