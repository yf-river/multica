package metrics

import "testing"

func TestEstimateUsageCostUSDMinimax(t *testing.T) {
	cost, ok := EstimateUsageCostUSD("minimax-m2.7-ioa", 1_000_000, 1_000_000, 100_000, 100_000)
	if !ok {
		t.Fatalf("expected minimax-m2.7-ioa to resolve")
	}
	if cost != 1.5435 {
		t.Fatalf("cost = %v, want 1.5435", cost)
	}
}

func TestEstimateUsageCostUSDDeepSeekV4ProIOA(t *testing.T) {
	cost, ok := EstimateUsageCostUSD("codebuddy/deepseek-v4-pro-ioa", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !ok {
		t.Fatalf("expected codebuddy/deepseek-v4-pro-ioa to resolve")
	}
	if cost != 1.743625 {
		t.Fatalf("cost = %v, want 1.743625", cost)
	}
}

func TestEstimateUsageCostUSDDeepSeekV4FlashIOA(t *testing.T) {
	cost, ok := EstimateUsageCostUSD("codebuddy/deepseek-v4-flash-ioa", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !ok {
		t.Fatalf("expected codebuddy/deepseek-v4-flash-ioa to resolve")
	}
	if cost != 0.5628 {
		t.Fatalf("cost = %v, want 0.5628", cost)
	}
}

func TestEstimateUsageCostUSDKimiK26IOA(t *testing.T) {
	cost, ok := EstimateUsageCostUSD("codebuddy/kimi-k2.6-ioa", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !ok {
		t.Fatalf("expected codebuddy/kimi-k2.6-ioa to resolve")
	}
	if cost != 6.06 {
		t.Fatalf("cost = %v, want 6.06", cost)
	}
}

func TestEstimateUsageCostUSDKimiK27IOA(t *testing.T) {
	cost, ok := EstimateUsageCostUSD("codebuddy/kimi-k2.7-ioa", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !ok {
		t.Fatalf("expected codebuddy/kimi-k2.7-ioa to resolve")
	}
	if cost != 6.09 {
		t.Fatalf("cost = %v, want 6.09", cost)
	}
}

func TestEstimateUsageCostUSDUnknownModel(t *testing.T) {
	cost, ok := EstimateUsageCostUSD("unknown-model", 1_000_000, 1_000_000, 0, 0)
	if ok {
		t.Fatalf("unexpected pricing for unknown model")
	}
	if cost != 0 {
		t.Fatalf("cost = %v, want 0", cost)
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
