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
