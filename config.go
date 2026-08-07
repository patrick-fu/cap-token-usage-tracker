package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultRetentionDays   = 30
	defaultFlushInterval   = 5 * time.Second
	defaultFlushMaxRecords = 100
)

type Config struct {
	DataPath        string
	RetentionDays   int
	FlushInterval   time.Duration
	FlushMaxRecords int
	SyncOnRecord    bool
	APIKeyAliases   []ConfigAPIKeyAlias
}

type configAPIKeyAliasYAML struct {
	APIKey string `yaml:"api_key"`
	Alias  string `yaml:"alias"`
}

type configYAML struct {
	DataPath        string                  `yaml:"data_path"`
	RetentionDays   *int                    `yaml:"retention_days"`
	FlushInterval   string                  `yaml:"flush_interval"`
	FlushMaxRecords *int                    `yaml:"flush_max_records"`
	SyncOnRecord    *bool                   `yaml:"sync_on_record"`
	APIKeyAliases   []configAPIKeyAliasYAML `yaml:"api_key_aliases"`
}

func defaultConfig() Config {
	return Config{
		DataPath:        resolvedDefaultDataPath(),
		RetentionDays:   defaultRetentionDays,
		FlushInterval:   defaultFlushInterval,
		FlushMaxRecords: defaultFlushMaxRecords,
		SyncOnRecord:    true,
	}
}

func parseConfig(raw []byte) (Config, error) {
	cfg := defaultConfig()
	if len(raw) == 0 {
		return normalizeConfig(cfg)
	}

	var input configYAML
	if err := yaml.Unmarshal(raw, &input); err != nil {
		return Config{}, fmt.Errorf("parse config YAML: %w", err)
	}
	if strings.TrimSpace(input.DataPath) != "" {
		cfg.DataPath = strings.TrimSpace(input.DataPath)
	}
	if input.RetentionDays != nil {
		cfg.RetentionDays = *input.RetentionDays
	}
	if strings.TrimSpace(input.FlushInterval) != "" {
		interval, err := time.ParseDuration(strings.TrimSpace(input.FlushInterval))
		if err != nil {
			return Config{}, fmt.Errorf("parse flush_interval: %w", err)
		}
		cfg.FlushInterval = interval
	}
	if input.FlushMaxRecords != nil {
		cfg.FlushMaxRecords = *input.FlushMaxRecords
	}
	if input.SyncOnRecord != nil {
		cfg.SyncOnRecord = *input.SyncOnRecord
	}
	for _, entry := range input.APIKeyAliases {
		cfg.APIKeyAliases = append(cfg.APIKeyAliases, ConfigAPIKeyAlias{
			APIKey: entry.APIKey,
			Alias:  entry.Alias,
		})
	}
	return normalizeConfig(cfg)
}

func normalizeConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.DataPath) == "" {
		return Config{}, fmt.Errorf("data_path must not be empty")
	}
	if cfg.RetentionDays < 1 || cfg.RetentionDays > 3650 {
		return Config{}, fmt.Errorf("retention_days must be between 1 and 3650")
	}
	if cfg.FlushInterval < time.Second || cfg.FlushInterval > time.Hour {
		return Config{}, fmt.Errorf("flush_interval must be between 1s and 1h")
	}
	if cfg.FlushMaxRecords < 1 || cfg.FlushMaxRecords > 1_000_000 {
		return Config{}, fmt.Errorf("flush_max_records must be between 1 and 1000000")
	}
	normalizedAliases, err := normalizeConfigAPIKeyAliases(cfg.APIKeyAliases)
	if err != nil {
		return Config{}, err
	}
	cfg.APIKeyAliases = normalizedAliases
	absolute, err := filepath.Abs(filepath.Clean(cfg.DataPath))
	if err != nil {
		return Config{}, fmt.Errorf("resolve data_path: %w", err)
	}
	cfg.DataPath = absolute
	return cfg, nil
}

// normalizeConfigAPIKeyAliases validates and normalizes configured downstream API
// key aliases. Raw keys are retained in-memory only; they are fingerprinted into
// SHA-256 identifiers at store-apply time and never persisted to bbolt.
func normalizeConfigAPIKeyAliases(entries []ConfigAPIKeyAlias) ([]ConfigAPIKeyAlias, error) {
	seenKeys := make(map[string]struct{}, len(entries))
	seenAliases := make(map[string]struct{}, len(entries))
	result := make([]ConfigAPIKeyAlias, 0, len(entries))
	for _, entry := range entries {
		apiKey := strings.TrimSpace(entry.APIKey)
		alias := normalizeAPIKeyAlias(entry.Alias)
		if apiKey == "" {
			return nil, fmt.Errorf("api_key_aliases: api_key must not be empty")
		}
		if alias == "" {
			return nil, fmt.Errorf("api_key_aliases: alias must not be empty")
		}
		if _, exists := seenKeys[apiKey]; exists {
			return nil, fmt.Errorf("api_key_aliases: duplicate api_key")
		}
		lowerAlias := strings.ToLower(alias)
		if _, exists := seenAliases[lowerAlias]; exists {
			return nil, fmt.Errorf("api_key_aliases: duplicate alias %q (case-insensitive)", alias)
		}
		seenKeys[apiKey] = struct{}{}
		seenAliases[lowerAlias] = struct{}{}
		result = append(result, ConfigAPIKeyAlias{APIKey: apiKey, Alias: alias})
	}
	return result, nil
}
