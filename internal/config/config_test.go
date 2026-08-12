package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigPermissionMask(t *testing.T) {
	for _, test := range []struct {
		mode    os.FileMode
		broader bool
	}{
		{0o640, false}, {0o600, false}, {0o400, false},
		{0o660, true}, {0o644, true}, {0o740, true},
	} {
		if got := permissionsBroaderThan(test.mode, 0o640); got != test.broader {
			t.Fatalf("mode %04o broader = %v, want %v", test.mode, got, test.broader)
		}
	}
}

func TestLoadAppliesDefaultsAndResolvesPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
  "collector": {"mock_file": "mock.json"},
  "credentials": [
    {"user_name":"alice","credential_name":"phone","uuid":"11111111-1111-4111-8111-111111111111","enabled":true}
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Path != filepath.Join(dir, "data", "audit.db") {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
	if cfg.Collector.MockFile != filepath.Join(dir, "mock.json") {
		t.Fatalf("mock path = %q", cfg.Collector.MockFile)
	}
	if cfg.Interval() != time.Minute {
		t.Fatalf("interval = %v", cfg.Interval())
	}
	if cfg.Display.Timezone != "Local" {
		t.Fatalf("timezone = %q", cfg.Display.Timezone)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted an unknown field")
	}
}

func TestValidateRejectsUnsupportedSource(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Database:  DatabaseConfig{Path: "audit.db"},
		Collector: CollectorConfig{Source: "v2ray", Interval: "60s"},
		Provider:  ProviderConfig{CycleStartDay: 1, TrafficDirection: "both"},
		Display:   DisplayConfig{Timezone: "UTC"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a source that is not implemented")
	}
}

func TestValidateMockDoesNotRequireV2RaySettings(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Database:  DatabaseConfig{Path: "audit.db"},
		Collector: CollectorConfig{Source: "mock", Interval: "60s", MockFile: "mock.json"},
		Provider:  ProviderConfig{CycleStartDay: 1, TrafficDirection: "both"},
		Display:   DisplayConfig{Timezone: "UTC"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadAppliesV2RayDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
  "collector": {"source":"sing-box-v2ray","interval":"60s"},
  "credentials": [
    {"user_name":"alice","credential_name":"phone","uuid":"11111111-1111-4111-8111-111111111111","stats_user":"alice-stats","enabled":true}
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Collector.V2RayAPI.Address != "127.0.0.1:10085" {
		t.Fatalf("V2Ray address = %q", cfg.Collector.V2RayAPI.Address)
	}
	if cfg.CollectorTimeout() != 5*time.Second {
		t.Fatalf("V2Ray timeout = %v", cfg.CollectorTimeout())
	}
	if cfg.Collector.V2RayAPI.ProcessName != "sing-box" {
		t.Fatalf("process name = %q", cfg.Collector.V2RayAPI.ProcessName)
	}
}

func TestValidateRejectsNonLoopbackV2RayAddress(t *testing.T) {
	t.Parallel()
	cfg := validV2RayConfig()
	cfg.Collector.V2RayAPI.Address = "192.0.2.1:10085"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a non-loopback V2Ray address")
	}
}

func TestValidateRequiresStatsUserForEnabledV2RayCredential(t *testing.T) {
	t.Parallel()
	cfg := validV2RayConfig()
	cfg.Credentials[0].StatsUser = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted an enabled credential without stats_user")
	}
}

func validV2RayConfig() Config {
	return Config{
		Database: DatabaseConfig{Path: "audit.db"},
		Collector: CollectorConfig{
			Source:   "sing-box-v2ray",
			Interval: "60s",
			V2RayAPI: V2RayAPIConfig{
				Address:     "127.0.0.1:10085",
				Timeout:     "5s",
				ProcessName: "sing-box",
			},
		},
		Provider: ProviderConfig{CycleStartDay: 1, TrafficDirection: "both"},
		Display:  DisplayConfig{Timezone: "UTC"},
		Credentials: []CredentialConfig{{
			UserName:       "alice",
			CredentialName: "phone",
			UUID:           "11111111-1111-4111-8111-111111111111",
			StatsUser:      "alice-stats",
			Enabled:        true,
		}},
	}
}
