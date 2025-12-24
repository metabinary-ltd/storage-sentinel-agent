package config

import "testing"

func TestDefaultLoad(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if cfg.API.Port == 0 {
		t.Fatalf("api port not set")
	}
	if cfg.Paths.DBPath == "" {
		t.Fatalf("db path empty")
	}
}
