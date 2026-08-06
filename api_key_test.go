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

func TestAPIKeyAliasesPersistApplyDynamicallyAndFilterQueries(t *testing.T) {
	config := testConfig(t)
	config.SyncOnRecord = true
	store, err := openStore(config)
	if err != nil {
		t.Fatal(err)
	}

	firstKey := "sk-customer-one-abcdef"
	secondKey := "sk-customer-two-abcdef"
	firstID, firstSuffix := apiKeyIdentity(firstKey)
	secondID, secondSuffix := apiKeyIdentity(secondKey)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	for _, dimensions := range []Dimensions{
		{Provider: "openai", Model: "m", Source: "cli", APIKeyID: firstID, APIKeySuffix: firstSuffix},
		{Provider: "openai", Model: "m", Source: "cli", APIKeyID: secondID, APIKeySuffix: secondSuffix},
	} {
		if err := store.Record(normalizedUsage{Dimensions: dimensions, RequestedAt: now, Counters: Counters{Requests: 1, InputTokens: 10, TotalTokens: 10}}); err != nil {
			t.Fatal(err)
		}
	}
	allStats, err := store.queryStatsBySourceAndAPIKeyAlias(usageRange{Name: "retention"}, "cli", "")
	if err != nil || len(allStats.Groups) != 2 {
		t.Fatalf("same-suffix keys collapsed into one group: %+v, %v", allStats.Groups, err)
	}
	if _, err := store.SaveModelPrices(map[string]ModelPrice{"m": {Input: 1}}); err != nil {
		t.Fatal(err)
	}
	alias, err := store.SetAPIKeyAlias(firstKey, "  Primary Customer  ")
	if err != nil || alias.Alias != "Primary Customer" || alias.APIKeySuffix != firstSuffix || alias.UpdatedAt.IsZero() {
		t.Fatalf("set alias = %+v, %v", alias, err)
	}
	if _, err := store.SetAPIKeyAlias(secondKey, "primary customer"); err == nil || errorHTTPStatus(err) != 400 {
		t.Fatalf("case-insensitive duplicate alias was accepted: %v", err)
	}

	queryRange := usageRange{Name: "retention"}
	stats, err := store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", "PRIMARY CUSTOMER")
	if err != nil || stats.Summary.Requests != 1 || len(stats.Groups) != 1 || stats.Groups[0].APIKeyAlias != "Primary Customer" {
		t.Fatalf("alias-filtered stats = %+v, %v", stats, err)
	}
	page, err := store.queryRequestPageByFilters(queryRange, 0, 100, "", "cli", "", "primary customer")
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].APIKeyAlias != "Primary Customer" || page.Items[0].APIKeySuffix != firstSuffix {
		t.Fatalf("alias-filtered requests = %+v, %v", page, err)
	}
	costs, err := store.queryCostsBySourceAndAPIKeyAlias(queryRange, "cli", "Primary Customer")
	if err != nil || costs.Summary.Requests != 1 {
		t.Fatalf("alias-filtered costs = %+v, %v", costs, err)
	}
	if _, err := store.ResolveAPIKeyAlias("primary customer"); err != nil {
		t.Fatalf("resolve alias: %v", err)
	}
	if _, err := store.SetAPIKeyAlias(firstKey, "Renamed"); err != nil {
		t.Fatal(err)
	}
	stats, err = store.queryStatsBySourceAndAPIKeyAlias(queryRange, "cli", "renamed")
	if err != nil || len(stats.Groups) != 1 || stats.Groups[0].APIKeyAlias != "Renamed" {
		t.Fatalf("renamed historical stats = %+v, %v", stats, err)
	}

	if err := store.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		raw := string(meta.Get(apiKeyAliasesKey))
		if strings.Contains(raw, firstKey) || !strings.Contains(raw, firstID) || !strings.Contains(raw, firstSuffix) {
			t.Fatalf("alias metadata leaked key or omitted fingerprint: %s", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err = store.queryRequestPageByFilters(queryRange, 0, 100, "", "cli", "", "RENAMED")
	if err != nil || page.Total != 1 || page.Items[0].APIKeyAlias != "Renamed" {
		t.Fatalf("restarted alias filter = %+v, %v", page, err)
	}
	public, err := json.Marshal(page)
	if err != nil || strings.Contains(string(public), firstID) || strings.Contains(string(public), firstKey) {
		t.Fatalf("request response leaked fingerprint or key: %s, %v", public, err)
	}
}
