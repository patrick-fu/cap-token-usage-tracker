package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
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

// ConfigAPIKeyAlias is a single plugin-config entry mapping a raw downstream API
// key to a human-readable alias. The raw key is fingerprinted at store-apply time
// and never persisted to bbolt.
type ConfigAPIKeyAlias struct {
	APIKey string
	Alias  string
}

// buildAPIKeyAliasRecords converts config-declared entries into in-memory
// fingerprint-keyed alias records. Returns an error on duplicate fingerprints
// (should not happen after config validation, but guards against collisions).
func buildAPIKeyAliasRecords(entries []ConfigAPIKeyAlias) (map[string]APIKeyAliasRecord, error) {
	records := make(map[string]APIKeyAliasRecord, len(entries))
	now := nowUTC()
	for _, entry := range entries {
		id, suffix := apiKeyIdentity(entry.APIKey)
		if id == "" {
			return nil, fmt.Errorf("api_key_aliases: api_key must not be empty")
		}
		if _, exists := records[id]; exists {
			return nil, fmt.Errorf("api_key_aliases: duplicate api_key fingerprint")
		}
		records[id] = APIKeyAliasRecord{
			APIKeyID:     id,
			APIKeySuffix: suffix,
			Alias:        normalizeAPIKeyAlias(entry.Alias),
			UpdatedAt:    now,
		}
	}
	return records, nil
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

// resolveAPIKeyAliasIDs resolves one or more alias names (case-insensitive) into
// the set of API key fingerprints they map to. Empty input (or empty entries)
// returns nil when no other alias is present. Any non-empty alias that does not
// match a configured record returns an error.
func resolveAPIKeyAliasIDs(aliases []string, records map[string]APIKeyAliasRecord) (map[string]struct{}, error) {
	if len(aliases) == 0 {
		return nil, nil
	}
	ids := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		alias = normalizeAPIKeyAlias(alias)
		if alias == "" {
			continue
		}
		record, ok := findAPIKeyAlias(records, alias)
		if !ok {
			return nil, withStatus(400, "API key alias %q was not found", normalizeAPIKeyAlias(alias))
		}
		ids[record.APIKeyID] = struct{}{}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, nil
}

// apiKeyIDSetKey produces a stable, sorted string representation of an API key ID
// set for use as a cache key component.
func apiKeyIDSetKey(ids map[string]struct{}) string {
	if len(ids) == 0 {
		return ""
	}
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
