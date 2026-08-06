package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// persistedDimensions and persistedRequestDetail are disk-only encodings. They
// retain the fingerprint for aggregation/filtering while public JSON omits it.
type persistedDimensions struct {
	Dimensions
	APIKeyID string `json:"api_key_id"`
}

type persistedRequestDetail struct {
	RequestDetail
	APIKeyID string `json:"api_key_id"`
}

func marshalStoredDimensions(dimensions Dimensions) ([]byte, error) {
	return json.Marshal(persistedDimensions{Dimensions: dimensions, APIKeyID: dimensions.APIKeyID})
}

func unmarshalStoredDimensions(data []byte, dimensions *Dimensions) error {
	var stored persistedDimensions
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	*dimensions = stored.Dimensions
	dimensions.APIKeyID = stored.APIKeyID
	return nil
}

func marshalStoredRequest(detail RequestDetail) ([]byte, error) {
	return json.Marshal(persistedRequestDetail{RequestDetail: detail, APIKeyID: detail.APIKeyID})
}

func unmarshalStoredRequest(data []byte, detail *RequestDetail) error {
	var stored persistedRequestDetail
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	*detail = stored.RequestDetail
	detail.APIKeyID = stored.APIKeyID
	return nil
}

// APIKeyAliasRecord is the persisted, non-secret representation of a downstream
// API key. APIKeyID is a SHA-256 fingerprint; the raw key is never stored.
type APIKeyAliasRecord struct {
	APIKeyID     string    `json:"-"`
	APIKeySuffix string    `json:"api_key_suffix"`
	Alias        string    `json:"alias"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// APIKeyAlias is the public view of a configured downstream key alias.
type APIKeyAlias struct {
	APIKeySuffix string    `json:"api_key_suffix"`
	Alias        string    `json:"alias"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func apiKeyIdentity(raw string) (id, suffix string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", digest), apiKeySuffix(raw)
}

func apiKeySuffix(raw string) string {
	runes := []rune(strings.TrimSpace(raw))
	if len(runes) <= 6 {
		// Never expose a short key in full. Normal production keys still expose
		// only their last six characters; short keys have no public suffix.
		return ""
	}
	return string(runes[len(runes)-6:])
}

func normalizeAPIKeyAlias(value string) string {
	return strings.TrimSpace(value)
}

func (r APIKeyAliasRecord) public() APIKeyAlias {
	return APIKeyAlias{APIKeySuffix: r.APIKeySuffix, Alias: r.Alias, UpdatedAt: r.UpdatedAt.UTC()}
}

func cloneAPIKeyAliases(input map[string]APIKeyAliasRecord) map[string]APIKeyAliasRecord {
	result := make(map[string]APIKeyAliasRecord, len(input))
	for id, record := range input {
		record.APIKeyID = id
		result[id] = record
	}
	return result
}

func findAPIKeyAlias(records map[string]APIKeyAliasRecord, alias string) (APIKeyAliasRecord, bool) {
	alias = normalizeAPIKeyAlias(alias)
	if alias == "" {
		return APIKeyAliasRecord{}, false
	}
	for id, record := range records {
		if strings.EqualFold(record.Alias, alias) {
			record.APIKeyID = id
			return record, true
		}
	}
	return APIKeyAliasRecord{}, false
}

func validateAPIKeyAliasRecords(records map[string]APIKeyAliasRecord) error {
	seen := make(map[string]string, len(records))
	for id, record := range records {
		if strings.TrimSpace(id) == "" || normalizeAPIKeyAlias(record.Alias) == "" {
			return fmt.Errorf("invalid API key alias record")
		}
		key := strings.ToLower(normalizeAPIKeyAlias(record.Alias))
		if other, exists := seen[key]; exists && other != id {
			return fmt.Errorf("API key alias %q is already in use", record.Alias)
		}
		seen[key] = id
	}
	return nil
}

func applyAPIKeyAlias(dimensions *Dimensions, aliases map[string]APIKeyAliasRecord) string {
	if dimensions.APIKeyID == "" {
		return ""
	}
	record, ok := aliases[dimensions.APIKeyID]
	if !ok {
		return ""
	}
	return record.Alias
}
