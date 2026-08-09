package main

import "testing"

func TestDashboardPreferencesAllowAPIKeyColumns(t *testing.T) {
	preferences, err := normalizeDashboardPreferences(DashboardPreferences{
		RequestPageSize:      10,
		DimensionPageSize:    10,
		HiddenRequestColumns: []string{"api_key_alias", "api_key_suffix", "cache_hit_rate"},
		HiddenDimensionColumns: []string{
			"api_key_alias", "api_key_suffix",
		},
		TimeRangeMode: "custom",
	})
	if err != nil {
		t.Fatalf("API key columns rejected by preferences: %v", err)
	}
	if len(preferences.HiddenRequestColumns) != 3 || len(preferences.HiddenDimensionColumns) != 2 {
		t.Fatalf("API key columns were not retained: %+v", preferences)
	}
}
