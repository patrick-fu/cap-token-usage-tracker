package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigAPIKeyAliases(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "usage.db")
	cfg, err := parseConfig([]byte(fmt.Sprintf(`data_path: %q
api_key_aliases:
  - api_key: "  sk-first  "
    alias: "  Personal  "
  - api_key: "sk-second"
    alias: "Team"
`, dataPath)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeyAliases) != 2 {
		t.Fatalf("alias count = %d, want 2", len(cfg.APIKeyAliases))
	}
	if got := cfg.APIKeyAliases[0]; got.APIKey != "sk-first" || got.Alias != "Personal" {
		t.Fatalf("first alias = %+v", got)
	}
	if got := cfg.APIKeyAliases[1]; got.APIKey != "sk-second" || got.Alias != "Team" {
		t.Fatalf("second alias = %+v", got)
	}
}

func TestParseConfigRejectsDuplicateAPIKeyAlias(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "usage.db")
	_, err := parseConfig([]byte(fmt.Sprintf(`data_path: %q
api_key_aliases:
  - api_key: "sk-first"
    alias: "Personal"
  - api_key: "sk-second"
    alias: "personal"
`, dataPath)))
	if err == nil || !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("duplicate alias error = %v", err)
	}
}
