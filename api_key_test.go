package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestAPIKeyIdentityKeepsOnlyFingerprintAndSuffix(t *testing.T) {
	raw := "sk-downstream-super-secret-123456"
	id, suffix := apiKeyIdentity(raw)
	if len(id) != 64 || suffix != "123456" {
		t.Fatalf("identity = %q, %q", id, suffix)
	}
	dimensions := Dimensions{APIKeyID: id, APIKeySuffix: suffix}
	public, err := json.Marshal(dimensions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), raw) || strings.Contains(string(public), id) || !strings.Contains(string(public), suffix) {
		t.Fatalf("public dimensions leaked identity: %s", public)
	}
	persisted, err := marshalStoredDimensions(dimensions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), raw) || !strings.Contains(string(persisted), id) {
		t.Fatalf("stored dimensions did not preserve only fingerprint: %s", persisted)
	}
	var restored Dimensions
	if err := unmarshalStoredDimensions(persisted, &restored); err != nil || restored != dimensions {
		t.Fatalf("stored dimension roundtrip = %+v, %v", restored, err)
	}
}

func TestAPIKeyIdentityDoesNotExposeShortKeys(t *testing.T) {
	for _, raw := range []string{"", "a", "123456"} {
		_, suffix := apiKeyIdentity(raw)
		if suffix != "" {
			t.Fatalf("short key %q exposed as suffix %q", raw, suffix)
		}
	}
}

func TestDecodeUsageCapturesAPIKeyFingerprintWithoutRetainingKey(t *testing.T) {
	rawKey := "sk-downstream-super-secret-123456"
	usage, err := decodeUsage([]byte(`{"api_key":"`+rawKey+`","requested_at":"2026-08-06T00:00:00Z"}`), time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	wantID, wantSuffix := apiKeyIdentity(rawKey)
	if usage.Dimensions.APIKeyID != wantID || usage.Dimensions.APIKeySuffix != wantSuffix {
		t.Fatalf("decoded API key identity = %+v", usage.Dimensions)
	}
	encoded, err := json.Marshal(usage)
	if err != nil || strings.Contains(string(encoded), rawKey) || strings.Contains(string(encoded), wantID) {
		t.Fatalf("normalized usage leaked key or fingerprint: %s, %v", encoded, err)
	}
}

func TestConfigDrivenAPIKeyAliasesApplyAndFilterQueries(t *testing.T) {
	config := testConfig(t)
	config.SyncOnRecord = true

	firstKey := "sk-customer-one-abcdef"
	secondKey := "sk-customer-two-abcdef"
	firstID, firstSuffix := apiKeyIdentity(firstKey)
	secondID, secondSuffix := apiKeyIdentity(secondKey)

	config.APIKeyAliases = []ConfigAPIKeyAlias{
		{APIKey: firstKey, Alias: "  Primary Customer  "},
		{APIKey: secondKey, Alias: "Secondary Customer"},
	}

	store, err := openStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	for _, dimensions := range []Dimensions{
		{Provider: "openai", Model: "m", Source: "cli", APIKeyID: firstID, APIKeySuffix: firstSuffix},
		{Provider: "openai", Model: "m", Source: "cli", APIKeyID: secondID, APIKeySuffix: secondSuffix},
	} {
		if err := store.Record(normalizedUsage{Dimensions: dimensions, RequestedAt: now, Counters: Counters{Requests: 1, InputTokens: 10, TotalTokens: 10}}); err != nil {
			t.Fatal(err)
		}
	}
	allStats, err := store.queryStatsBySourceAndAPIKeyAlias(usageRange{Name: "retention"}, "cli", nil)
	if err != nil || len(allStats.Groups) != 2 {
		t.Fatalf("same-suffix keys collapsed into one group: %+v, %v", allStats.Groups, err)
	}

	if _, err := store.SaveModelPrices(map[string]ModelPrice{"m": {Input: 1}}); err != nil {
		t.Fatal(err)
	}

	queryRange := usageRange{Name: "retention"}

	// Single-alias filter.
	stats, err := store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"PRIMARY CUSTOMER"})
	if err != nil || stats.Summary.Requests != 1 || len(stats.Groups) != 1 || stats.Groups[0].APIKeyAlias != "Primary Customer" {
		t.Fatalf("alias-filtered stats = %+v, %v", stats, err)
	}
	page, err := store.queryRequestPageByFilters(queryRange, 0, 100, "", "cli", "", []string{"primary customer"})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].APIKeyAlias != "Primary Customer" || page.Items[0].APIKeySuffix != firstSuffix {
		t.Fatalf("alias-filtered requests = %+v, %v", page, err)
	}
	costs, err := store.queryCostsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"Primary Customer"})
	if err != nil || costs.Summary.Requests != 1 {
		t.Fatalf("alias-filtered costs = %+v, %v", costs, err)
	}

	// Multi-alias (OR union) filter — both keys.
	stats, err = store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"primary customer", "secondary customer"})
	if err != nil || stats.Summary.Requests != 2 || len(stats.Groups) != 2 {
		t.Fatalf("multi-alias union stats = %+v, %v", stats, err)
	}
	page, err = store.queryRequestPageByFilters(queryRange, 0, 100, "", "cli", "", []string{"primary customer", "secondary customer"})
	if err != nil || page.Total != 2 {
		t.Fatalf("multi-alias union requests = %+v, %v", page, err)
	}
	costs, err = store.queryCostsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"Primary Customer", "Secondary Customer"})
	if err != nil || costs.Summary.Requests != 2 {
		t.Fatalf("multi-alias union costs = %+v, %v", costs, err)
	}

	// Unknown alias → error.
	if _, err := store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"nonexistent"}); err == nil || errorHTTPStatus(err) != 400 {
		t.Fatalf("unknown alias should error 400: %v", err)
	}

	// Raw key is never persisted to bbolt.
	if err := store.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta == nil {
			return nil
		}
		raw := string(meta.Get(apiKeyAliasesKey))
		if strings.Contains(raw, firstKey) || strings.Contains(raw, secondKey) {
			t.Fatalf("alias metadata persisted raw key to bbolt: %s", raw)
		}
		if raw != "" {
			t.Fatalf("legacy alias key should be empty after migration, got: %s", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Public response never leaks fingerprint or raw key.
	public, err := json.Marshal(page)
	if err != nil || strings.Contains(string(public), firstID) || strings.Contains(string(public), firstKey) {
		t.Fatalf("request response leaked fingerprint or key: %s, %v", public, err)
	}
}

func TestConfigDrivenAPIKeyAliasesSurviveRestart(t *testing.T) {
	config := testConfig(t)
	config.SyncOnRecord = true
	config.APIKeyAliases = []ConfigAPIKeyAlias{
		{APIKey: "sk-customer-one-abcdef", Alias: "Primary"},
	}

	store, err := openStore(config)
	if err != nil {
		t.Fatal(err)
	}
	firstID, firstSuffix := apiKeyIdentity("sk-customer-one-abcdef")
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	if err := store.Record(normalizedUsage{Dimensions: Dimensions{Provider: "openai", Model: "m", Source: "cli", APIKeyID: firstID, APIKeySuffix: firstSuffix}, RequestedAt: now, Counters: Counters{Requests: 1, TotalTokens: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen with same config — aliases should reload from config.
	store, err = openStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	queryRange := usageRange{Name: "retention"}
	page, err := store.queryRequestPageByFilters(queryRange, 0, 100, "", "cli", "", []string{"PRIMARY"})
	if err != nil || page.Total != 1 || page.Items[0].APIKeyAlias != "Primary" {
		t.Fatalf("restarted alias filter = %+v, %v", page, err)
	}
}

func TestConfigDrivenAPIKeyAliasesReconfigure(t *testing.T) {
	config := testConfig(t)
	config.SyncOnRecord = true
	firstKey := "sk-customer-one-abcdef"
	secondKey := "sk-customer-two-abcdef"
	config.APIKeyAliases = []ConfigAPIKeyAlias{
		{APIKey: firstKey, Alias: "First"},
	}

	store, err := openStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	firstID, _ := apiKeyIdentity(firstKey)
	secondID, _ := apiKeyIdentity(secondKey)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	for _, dim := range []Dimensions{
		{Provider: "openai", Model: "m", Source: "cli", APIKeyID: firstID},
		{Provider: "openai", Model: "m", Source: "cli", APIKeyID: secondID},
	} {
		if err := store.Record(normalizedUsage{Dimensions: dim, RequestedAt: now, Counters: Counters{Requests: 1, TotalTokens: 5}}); err != nil {
			t.Fatal(err)
		}
	}

	queryRange := usageRange{Name: "retention"}
	// Initially only "First" alias exists — filtering by it returns 1 request.
	stats, err := store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"First"})
	if err != nil || stats.Summary.Requests != 1 {
		t.Fatalf("before reconfigure stats = %+v, %v", stats, err)
	}

	// Reconfigure: replace alias "First" with "Second".
	newConfig := config
	newConfig.APIKeyAliases = []ConfigAPIKeyAlias{
		{APIKey: secondKey, Alias: "Second"},
	}
	if err := store.Reconfigure(newConfig); err != nil {
		t.Fatal(err)
	}

	// Old alias no longer resolves.
	if _, err := store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"First"}); err == nil || errorHTTPStatus(err) != 400 {
		t.Fatalf("old alias should be gone after reconfigure: %v", err)
	}
	// New alias resolves and matches the second key's data.
	stats, err = store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", []string{"Second"})
	if err != nil || stats.Summary.Requests != 1 {
		t.Fatalf("after reconfigure stats = %+v, %v", stats, err)
	}
}

func TestConfigAPIKeyAliasesValidation(t *testing.T) {
	tests := []struct {
		name    string
		entries []ConfigAPIKeyAlias
		wantErr string
	}{
		{"empty api_key", []ConfigAPIKeyAlias{{APIKey: "", Alias: "A"}}, "api_key must not be empty"},
		{"empty alias", []ConfigAPIKeyAlias{{APIKey: "sk-1234567890", Alias: "  "}}, "alias must not be empty"},
		{"duplicate key", []ConfigAPIKeyAlias{{APIKey: "sk-same", Alias: "A"}, {APIKey: "sk-same", Alias: "B"}}, "duplicate api_key"},
		{"duplicate alias case-insensitive", []ConfigAPIKeyAlias{{APIKey: "sk-key1", Alias: "MyAlias"}, {APIKey: "sk-key2", Alias: "myalias"}}, "duplicate alias"},
		{"valid", []ConfigAPIKeyAlias{{APIKey: "sk-key1", Alias: "A"}, {APIKey: "sk-key2", Alias: "B"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeConfigAPIKeyAliases(tt.entries)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
				}
			}
		})
	}
}
