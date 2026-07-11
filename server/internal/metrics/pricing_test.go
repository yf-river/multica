package metrics

import "testing"

func TestEstimateUsageCostBreakdownUSDAliases(t *testing.T) {
	tests := []struct {
		model      string
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
		want       float64
	}{
		{model: "minimax-m2.7-ioa", input: 1_000_000, output: 1_000_000, cacheRead: 100_000, cacheWrite: 100_000, want: 1.5435},
		{model: "codebuddy/deepseek-v4-pro-ioa", input: 2_000_000, output: 1_000_000, cacheRead: 1_000_000, cacheWrite: 1_000_000, want: 1.308625},
		{model: "codex/gpt-5.3-codex-spark", input: 1_000_000, output: 1_000_000, cacheRead: 1_000_000, cacheWrite: 1_000_000, want: 16.1},
		{model: "codebuddy/deepseek-v4-flash-ioa", input: 2_000_000, output: 1_000_000, cacheRead: 1_000_000, cacheWrite: 1_000_000, want: 0.4228},
		{model: "codebuddy/kimi-k2.6-ioa", input: 2_000_000, output: 1_000_000, cacheRead: 1_000_000, cacheWrite: 1_000_000, want: 5.11},
		{model: "codebuddy/kimi-k2.7-ioa", input: 2_000_000, output: 1_000_000, cacheRead: 1_000_000, cacheWrite: 1_000_000, want: 5.14},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			breakdown, ok := EstimateUsageCostBreakdownUSD("", tt.model, tt.input, tt.output, tt.cacheRead, tt.cacheWrite)
			if !ok {
				t.Fatalf("expected %s to resolve", tt.model)
			}
			if breakdown.TotalCostUSD != tt.want {
				t.Fatalf("total = %v, want %v", breakdown.TotalCostUSD, tt.want)
			}
		})
	}
}

func TestEstimateUsageCostBreakdownUSDCodeBuddyOfficialSamples(t *testing.T) {
	tests := []struct {
		name       string
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
		want       float64
	}{
		{name: "platform-first-run", input: 59_738, output: 186, cacheRead: 29_440, cacheWrite: 30_298, want: 0.013449},
		{name: "platform-first-run-legacy-uncached-input", input: 0, output: 186, cacheRead: 29_440, cacheWrite: 30_298, want: 0.013449},
		{name: "platform-follow-up", input: 30_897, output: 9, cacheRead: 29_440, cacheWrite: 1_457, want: 0.000749},
		{name: "direct-a", input: 11_493, output: 6, cacheRead: 0, cacheWrite: 11_493, want: 0.005004},
		{name: "direct-b", input: 11_494, output: 6, cacheRead: 0, cacheWrite: 11_494, want: 0.005005},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			breakdown, ok := EstimateUsageCostBreakdownUSD("codebuddy", "deepseek-v4-pro-ioa", tt.input, tt.output, tt.cacheRead, tt.cacheWrite)
			if !ok {
				t.Fatalf("expected codebuddy/deepseek-v4-pro-ioa to resolve")
			}
			if breakdown.TotalCostUSD != tt.want {
				t.Fatalf("total = %v, want %v", breakdown.TotalCostUSD, tt.want)
			}
			if breakdown.CacheWriteCostUSD != 0 {
				t.Fatalf("cache write cost = %v, want 0", breakdown.CacheWriteCostUSD)
			}
		})
	}
}

func TestEstimateUsageCostBreakdownUSDProviderGenericModel(t *testing.T) {
	breakdown, ok := EstimateUsageCostBreakdownUSD("cursor", "auto", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !ok {
		t.Fatalf("expected cursor/auto to resolve")
	}
	if breakdown.TotalCostUSD != 7.5 {
		t.Fatalf("total = %v, want 7.5", breakdown.TotalCostUSD)
	}
	if breakdown.CacheWriteCostUSD != 0 {
		t.Fatalf("cache write = %v, want 0", breakdown.CacheWriteCostUSD)
	}
	if breakdown.CacheSavingsUSD != 1 {
		t.Fatalf("cache savings = %v, want 1", breakdown.CacheSavingsUSD)
	}
}

func TestEstimateUsageCostBreakdownUSDUnknownModel(t *testing.T) {
	breakdown, ok := EstimateUsageCostBreakdownUSD("", "unknown-model", 1_000_000, 1_000_000, 0, 0)
	if ok {
		t.Fatalf("unexpected pricing for unknown model")
	}
	if breakdown != (UsageCostBreakdown{}) {
		t.Fatalf("breakdown = %+v, want zero value", breakdown)
	}
}

func TestCanonicalModelPriceKey(t *testing.T) {
	provider, model, ok := CanonicalModelPriceKey("minimax/minimax-m2.7")
	if !ok {
		t.Fatalf("expected minimax alias to resolve")
	}
	if provider != "minimax" || model != "m2.7" {
		t.Fatalf("key = %s/%s, want minimax/m2.7", provider, model)
	}
}

func TestCanonicalModelPriceKeyKimiIOA(t *testing.T) {
	provider, model, ok := CanonicalModelPriceKey("codebuddy/kimi-k2.7-ioa")
	if !ok {
		t.Fatalf("expected kimi alias to resolve")
	}
	if provider != "kimi" || model != "k2.7-code" {
		t.Fatalf("key = %s/%s, want kimi/k2.7-code", provider, model)
	}
}
