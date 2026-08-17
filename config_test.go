package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigValid(t *testing.T) {
	cfg := defaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if _, ok := cfg.Selected(); !ok {
		t.Fatal("default config has no selected template")
	}
}

func TestConfigValidateRules(t *testing.T) {
	base := func() *Config {
		cfg := defaultConfig()
		cfg.Placeholders = []Placeholder{{Name: "account_id", Path: "$.account_id"}}
		return cfg
	}

	cfg := base()
	cfg.Templates = nil
	if err := cfg.Validate(); err == nil {
		t.Error("empty template list must be rejected")
	}

	cfg = base()
	cfg.Templates = append(cfg.Templates, Template{ID: "dup", Name: cfg.Templates[0].Name, URL: "http://x.{account}:t@h:1"})
	if err := cfg.Validate(); err == nil {
		t.Error("duplicate template names must be rejected")
	}

	cfg = base()
	cfg.Templates[0].URL = "ftp://bad:21"
	if err := cfg.Validate(); err == nil {
		t.Error("invalid template URL must be rejected")
	}

	cfg = base()
	cfg.Templates[0].URL = "" // draft URL is allowed
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty template URL should be allowed, got %v", err)
	}

	cfg = base()
	cfg.SelectedTemplate = "missing"
	if err := cfg.Validate(); err == nil {
		t.Error("missing selected template must be rejected")
	}

	cfg = base()
	cfg.Placeholders = append(cfg.Placeholders, Placeholder{Name: "account_id", Path: "$.x"})
	if err := cfg.Validate(); err == nil {
		t.Error("duplicate placeholder names must be rejected")
	}

	cfg = base()
	cfg.Placeholders = append(cfg.Placeholders, Placeholder{Name: "builtin_same", Path: "$.x"})
	cfg.Placeholders = append(cfg.Placeholders, Placeholder{Name: "builtin_same", Path: "$.y"})
	if err := cfg.Validate(); err == nil {
		t.Error("duplicate placeholder names must be rejected (second copy)")
	}

	cfg = base()
	cfg.Placeholders = append(cfg.Placeholders, Placeholder{Name: "bad-name", Path: "$.x"})
	if err := cfg.Validate(); err == nil {
		t.Error("non-word placeholder names must be rejected")
	}

	cfg = base()
	cfg.WriteConcurrency = 0
	cfg.normalize()
	if cfg.WriteConcurrency != defaultWriteConcurrency {
		t.Errorf("normalize should default concurrency, got %d", cfg.WriteConcurrency)
	}
	cfg.WriteConcurrency = maxWriteConcurrency + 1
	if err := cfg.Validate(); err == nil {
		t.Error("out-of-range concurrency must be rejected")
	}
}

func TestConfigSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", dir)

	store, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg := defaultConfig()
	cfg.Templates = []Template{{ID: "only", Name: "唯一模板", URL: "http://Default.{account}:token@resin-host:2260"}}
	cfg.SelectedTemplate = "only"
	cfg.WriteConcurrency = 16
	if err := store.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Snapshot()
	if len(got.Templates) != 1 || got.Templates[0].Name != "唯一模板" {
		t.Fatalf("roundtrip templates = %+v", got.Templates)
	}
	if got.WriteConcurrency != 16 {
		t.Fatalf("roundtrip concurrency = %d", got.WriteConcurrency)
	}

	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("config file missing after save: %v", err)
	}
}
