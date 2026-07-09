package metrics

import (
	"math"
	"regexp"
	"strings"
)

type ModelPrice struct {
	Provider      string
	Model         string
	InputPerM     float64
	CacheReadPerM float64
	// CacheWritePerM is used when runtimes report cache creation/write tokens.
	// For providers with only cache-hit/cache-miss input pricing, this is the
	// cache-miss input rate, not a separate surcharge.
	CacheWritePerM float64
	OutputPerM     float64
}

type UsageCostBreakdown struct {
	InputCostUSD      float64
	OutputCostUSD     float64
	CacheReadCostUSD  float64
	CacheWriteCostUSD float64
	CacheSavingsUSD   float64
	TotalCostUSD      float64
}

var modelPrices = map[string]ModelPrice{
	"openai:gpt-5.5":              {Provider: "openai", Model: "gpt-5.5", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 0.50, OutputPerM: 30.00},
	"openai:gpt-5.4":              {Provider: "openai", Model: "gpt-5.4", InputPerM: 2.50, CacheReadPerM: 0.25, CacheWritePerM: 0.25, OutputPerM: 15.00},
	"openai:gpt-5.4-mini":         {Provider: "openai", Model: "gpt-5.4-mini", InputPerM: 0.75, CacheReadPerM: 0.075, CacheWritePerM: 0.075, OutputPerM: 4.50},
	"openai:gpt-5.3-codex-spark":  {Provider: "openai", Model: "gpt-5.3-codex-spark", InputPerM: 1.75, CacheReadPerM: 0.175, CacheWritePerM: 0.175, OutputPerM: 14.00},
	"openai:gpt-5.3-codex":        {Provider: "openai", Model: "gpt-5.3-codex", InputPerM: 1.75, CacheReadPerM: 0.175, CacheWritePerM: 0.175, OutputPerM: 14.00},
	"openai:gpt-5.2-codex":        {Provider: "openai", Model: "gpt-5.2-codex", InputPerM: 1.75, CacheReadPerM: 0.175, CacheWritePerM: 0.175, OutputPerM: 14.00},
	"openai:gpt-5-codex":          {Provider: "openai", Model: "gpt-5-codex", InputPerM: 1.25, CacheReadPerM: 0.125, CacheWritePerM: 0.125, OutputPerM: 10.00},
	"openai:gpt-5":                {Provider: "openai", Model: "gpt-5", InputPerM: 1.25, CacheReadPerM: 0.125, CacheWritePerM: 0.125, OutputPerM: 10.00},
	"openai:gpt-5-mini":           {Provider: "openai", Model: "gpt-5-mini", InputPerM: 0.25, CacheReadPerM: 0.025, CacheWritePerM: 0.025, OutputPerM: 2.00},
	"openai:gpt-5-nano":           {Provider: "openai", Model: "gpt-5-nano", InputPerM: 0.05, CacheReadPerM: 0.005, CacheWritePerM: 0.005, OutputPerM: 0.40},
	"openai:o3-mini":              {Provider: "openai", Model: "o3-mini", InputPerM: 1.10, CacheReadPerM: 0.55, CacheWritePerM: 0.55, OutputPerM: 4.40},
	"openai:o3":                   {Provider: "openai", Model: "o3", InputPerM: 2.00, CacheReadPerM: 0.50, CacheWritePerM: 0.50, OutputPerM: 8.00},
	"openai:o4-mini":              {Provider: "openai", Model: "o4-mini", InputPerM: 1.10, CacheReadPerM: 0.275, CacheWritePerM: 0.275, OutputPerM: 4.40},
	"openai:gpt-4o-mini":          {Provider: "openai", Model: "gpt-4o-mini", InputPerM: 0.15, CacheReadPerM: 0.075, CacheWritePerM: 0.075, OutputPerM: 0.60},
	"openai:gpt-4o":               {Provider: "openai", Model: "gpt-4o", InputPerM: 2.50, CacheReadPerM: 1.25, CacheWritePerM: 1.25, OutputPerM: 10.00},
	"anthropic:claude-fable-5":    {Provider: "anthropic", Model: "claude-fable-5", InputPerM: 10.00, CacheReadPerM: 1.00, CacheWritePerM: 12.50, OutputPerM: 50.00},
	"anthropic:claude-opus-4.8":   {Provider: "anthropic", Model: "claude-opus-4.8", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-opus-4.7":   {Provider: "anthropic", Model: "claude-opus-4.7", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-opus-4.6":   {Provider: "anthropic", Model: "claude-opus-4.6", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-opus-4.5":   {Provider: "anthropic", Model: "claude-opus-4.5", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-sonnet-4.6": {Provider: "anthropic", Model: "claude-sonnet-4.6", InputPerM: 3.00, CacheReadPerM: 0.30, CacheWritePerM: 3.75, OutputPerM: 15.00},
	"anthropic:claude-sonnet-4.5": {Provider: "anthropic", Model: "claude-sonnet-4.5", InputPerM: 3.00, CacheReadPerM: 0.30, CacheWritePerM: 3.75, OutputPerM: 15.00},
	"anthropic:claude-sonnet-4":   {Provider: "anthropic", Model: "claude-sonnet-4", InputPerM: 3.00, CacheReadPerM: 0.30, CacheWritePerM: 3.75, OutputPerM: 15.00},
	"anthropic:claude-haiku-4.5":  {Provider: "anthropic", Model: "claude-haiku-4.5", InputPerM: 1.00, CacheReadPerM: 0.10, CacheWritePerM: 1.25, OutputPerM: 5.00},
	"anthropic:claude-haiku-3.5":  {Provider: "anthropic", Model: "claude-haiku-3.5", InputPerM: 0.80, CacheReadPerM: 0.08, CacheWritePerM: 1.00, OutputPerM: 4.00},
	"deepseek:v4-pro":             {Provider: "deepseek", Model: "v4-pro", InputPerM: 0.435, CacheReadPerM: 0.003625, CacheWritePerM: 0.435, OutputPerM: 0.87},
	"deepseek:v4-flash":           {Provider: "deepseek", Model: "v4-flash", InputPerM: 0.14, CacheReadPerM: 0.0028, CacheWritePerM: 0.14, OutputPerM: 0.28},
	"kimi:k2.6":                   {Provider: "kimi", Model: "k2.6", InputPerM: 0.95, CacheReadPerM: 0.16, CacheWritePerM: 0.95, OutputPerM: 4.00},
	"kimi:k2.7-code":              {Provider: "kimi", Model: "k2.7-code", InputPerM: 0.95, CacheReadPerM: 0.19, CacheWritePerM: 0.95, OutputPerM: 4.00},
	"zhipu:glm-5.1":               {Provider: "zhipu", Model: "glm-5.1", InputPerM: 1.40, CacheReadPerM: 0.26, CacheWritePerM: 1.40, OutputPerM: 4.40},
	"zhipu:glm-5":                 {Provider: "zhipu", Model: "glm-5", InputPerM: 1.00, CacheReadPerM: 0.20, CacheWritePerM: 1.00, OutputPerM: 3.20},
	"zhipu:glm-5-turbo":           {Provider: "zhipu", Model: "glm-5-turbo", InputPerM: 1.20, CacheReadPerM: 0.24, CacheWritePerM: 1.20, OutputPerM: 4.00},
	"zhipu:glm-4.7":               {Provider: "zhipu", Model: "glm-4.7", InputPerM: 0.60, CacheReadPerM: 0.11, CacheWritePerM: 0.60, OutputPerM: 2.20},
	"zhipu:glm-4.7-flashx":        {Provider: "zhipu", Model: "glm-4.7-flashx", InputPerM: 0.07, CacheReadPerM: 0.01, CacheWritePerM: 0.07, OutputPerM: 0.40},
	"zhipu:glm-4.7-flash":         {Provider: "zhipu", Model: "glm-4.7-flash", InputPerM: 0, CacheReadPerM: 0, CacheWritePerM: 0, OutputPerM: 0},
	"zhipu:glm-4.6":               {Provider: "zhipu", Model: "glm-4.6", InputPerM: 0.60, CacheReadPerM: 0.11, CacheWritePerM: 0.60, OutputPerM: 2.20},
	"zhipu:glm-4.5":               {Provider: "zhipu", Model: "glm-4.5", InputPerM: 0.60, CacheReadPerM: 0.11, CacheWritePerM: 0.60, OutputPerM: 2.20},
	"zhipu:glm-4.5-x":             {Provider: "zhipu", Model: "glm-4.5-x", InputPerM: 2.20, CacheReadPerM: 0.45, CacheWritePerM: 2.20, OutputPerM: 8.90},
	"zhipu:glm-4.5-air":           {Provider: "zhipu", Model: "glm-4.5-air", InputPerM: 0.20, CacheReadPerM: 0.03, CacheWritePerM: 0.20, OutputPerM: 1.10},
	"zhipu:glm-4.5-airx":          {Provider: "zhipu", Model: "glm-4.5-airx", InputPerM: 1.10, CacheReadPerM: 0.22, CacheWritePerM: 1.10, OutputPerM: 4.50},
	"zhipu:glm-4.5-flash":         {Provider: "zhipu", Model: "glm-4.5-flash", InputPerM: 0, CacheReadPerM: 0, CacheWritePerM: 0, OutputPerM: 0},
	"cursor:auto":                 {Provider: "cursor", Model: "auto", InputPerM: 1.25, CacheReadPerM: 0.25, CacheWritePerM: 0, OutputPerM: 6.00},
	"cursor:composer-2.5-fast":    {Provider: "cursor", Model: "composer-2.5-fast", InputPerM: 3.00, CacheReadPerM: 0.50, CacheWritePerM: 0, OutputPerM: 15.00},
	"cursor:composer-2.5":         {Provider: "cursor", Model: "composer-2.5", InputPerM: 0.50, CacheReadPerM: 0.20, CacheWritePerM: 0, OutputPerM: 2.50},
	"cursor:composer-2-fast":      {Provider: "cursor", Model: "composer-2-fast", InputPerM: 1.50, CacheReadPerM: 0.35, CacheWritePerM: 0, OutputPerM: 7.50},
	"cursor:composer-2":           {Provider: "cursor", Model: "composer-2", InputPerM: 0.50, CacheReadPerM: 0.20, CacheWritePerM: 0, OutputPerM: 2.50},
	"cursor:composer-1.5":         {Provider: "cursor", Model: "composer-1.5", InputPerM: 3.50, CacheReadPerM: 0.35, CacheWritePerM: 0, OutputPerM: 17.50},
	"cursor:composer-1":           {Provider: "cursor", Model: "composer-1", InputPerM: 1.25, CacheReadPerM: 0.125, CacheWritePerM: 0, OutputPerM: 10.00},
	"minimax:m2.7":                {Provider: "minimax", Model: "m2.7", InputPerM: 0.30, CacheReadPerM: 0.06, CacheWritePerM: 0.375, OutputPerM: 1.20},
	"minimax:m2.7-highspeed":      {Provider: "minimax", Model: "m2.7-highspeed", InputPerM: 0.60, CacheReadPerM: 0.06, CacheWritePerM: 0.375, OutputPerM: 2.40},
	"google:gemini-3-flash":       {Provider: "google", Model: "gemini-3-flash", InputPerM: 0.50, CacheReadPerM: 0.05, CacheWritePerM: 0.50, OutputPerM: 3.00},
	"google:gemini-3.1-pro":       {Provider: "google", Model: "gemini-3.1-pro", InputPerM: 2.00, CacheReadPerM: 0.20, CacheWritePerM: 2.00, OutputPerM: 12.00},
	"google:gemini-2.5-pro":       {Provider: "google", Model: "gemini-2.5-pro", InputPerM: 1.25, CacheReadPerM: 0.31, CacheWritePerM: 1.25, OutputPerM: 10.00},
	"google:gemini-2.5-flash":     {Provider: "google", Model: "gemini-2.5-flash", InputPerM: 0.30, CacheReadPerM: 0.03, CacheWritePerM: 0.30, OutputPerM: 2.50},
}

var modelAliasRules = []struct {
	re       *regexp.Regexp
	priceKey string
}{
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]5$|^gpt-5-5$`), "openai:gpt-5.5"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]4($|-2026-03-05|-xhigh)`), "openai:gpt-5.4"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]4-mini($|[^a-z0-9])`), "openai:gpt-5.4-mini"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]3-codex-spark$`), "openai:gpt-5.3-codex-spark"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]3-codex$`), "openai:gpt-5.3-codex"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]2-codex$`), "openai:gpt-5.2-codex"},
	{regexp.MustCompile(`(^|/|:)gpt-5-codex$`), "openai:gpt-5-codex"},
	{regexp.MustCompile(`(^|/|:)gpt-5-mini($|[^a-z0-9])`), "openai:gpt-5-mini"},
	{regexp.MustCompile(`(^|/|:)gpt-5-nano($|[^a-z0-9])`), "openai:gpt-5-nano"},
	{regexp.MustCompile(`(^|/|:)gpt-5($|-20\d{2}-\d{2}-\d{2}|-latest)`), "openai:gpt-5"},
	{regexp.MustCompile(`(^|/|:)o3-mini($|[^a-z0-9])`), "openai:o3-mini"},
	{regexp.MustCompile(`(^|/|:)o3($|[^a-z0-9])`), "openai:o3"},
	{regexp.MustCompile(`(^|/|:)o4-mini($|[^a-z0-9])`), "openai:o4-mini"},
	{regexp.MustCompile(`(^|/|:)gpt-4o-mini($|[^a-z0-9])`), "openai:gpt-4o-mini"},
	{regexp.MustCompile(`(^|/|:)gpt-4o($|[^a-z0-9])`), "openai:gpt-4o"},
	{regexp.MustCompile(`claude-fable-5`), "anthropic:claude-fable-5"},
	{regexp.MustCompile(`claude-opus-4[-.]8`), "anthropic:claude-opus-4.8"},
	{regexp.MustCompile(`claude-opus-4[-.]7`), "anthropic:claude-opus-4.7"},
	{regexp.MustCompile(`claude-opus-4[-.]6`), "anthropic:claude-opus-4.6"},
	{regexp.MustCompile(`claude-opus-4[-.]5`), "anthropic:claude-opus-4.5"},
	{regexp.MustCompile(`claude-sonnet-4[-.]6|claude-4[-.]6-sonnet`), "anthropic:claude-sonnet-4.6"},
	{regexp.MustCompile(`claude-sonnet-4[-.]5|claude-4[-.]5-sonnet`), "anthropic:claude-sonnet-4.5"},
	{regexp.MustCompile(`claude-sonnet-4($|[^-.0-9])|claude-4-sonnet`), "anthropic:claude-sonnet-4"},
	{regexp.MustCompile(`claude-haiku-4[-.]5`), "anthropic:claude-haiku-4.5"},
	{regexp.MustCompile(`claude-haiku-3[-.]5`), "anthropic:claude-haiku-3.5"},
	{regexp.MustCompile(`deepseek-v4-pro`), "deepseek:v4-pro"},
	{regexp.MustCompile(`deepseek-v4-flash|^deepseek-chat$|^deepseek-reasoner$`), "deepseek:v4-flash"},
	{regexp.MustCompile(`kimi-k2[.]7(-code)?(-ioa)?`), "kimi:k2.7-code"},
	{regexp.MustCompile(`kimi-k2[.]6(-ioa)?`), "kimi:k2.6"},
	{regexp.MustCompile(`glm-5[.]1`), "zhipu:glm-5.1"},
	{regexp.MustCompile(`glm-5-turbo`), "zhipu:glm-5-turbo"},
	{regexp.MustCompile(`glm-5($|[^-.0-9])`), "zhipu:glm-5"},
	{regexp.MustCompile(`glm-4[.]7-flashx`), "zhipu:glm-4.7-flashx"},
	{regexp.MustCompile(`glm-4[.]7-flash`), "zhipu:glm-4.7-flash"},
	{regexp.MustCompile(`glm-4[.]7`), "zhipu:glm-4.7"},
	{regexp.MustCompile(`glm-4[.]6`), "zhipu:glm-4.6"},
	{regexp.MustCompile(`glm-4[.]5-airx`), "zhipu:glm-4.5-airx"},
	{regexp.MustCompile(`glm-4[.]5-air`), "zhipu:glm-4.5-air"},
	{regexp.MustCompile(`glm-4[.]5-flash`), "zhipu:glm-4.5-flash"},
	{regexp.MustCompile(`glm-4[.]5-x`), "zhipu:glm-4.5-x"},
	{regexp.MustCompile(`glm-4[.]5`), "zhipu:glm-4.5"},
	{regexp.MustCompile(`^cursor/(auto|composer-2[.]5-fast|composer-2[.]5|composer-2-fast|composer-2|composer-1[.]5|composer-1)$`), "cursor:$1"},
	{regexp.MustCompile(`^cursor$`), "cursor:composer-2.5-fast"},
	{regexp.MustCompile(`minimax-m2[.]7.*highspeed|highspeed.*minimax-m2[.]7`), "minimax:m2.7-highspeed"},
	{regexp.MustCompile(`minimax-m2[.]7`), "minimax:m2.7"},
	{regexp.MustCompile(`gemini-3-flash`), "google:gemini-3-flash"},
	{regexp.MustCompile(`gemini-3[.]1-pro`), "google:gemini-3.1-pro"},
	{regexp.MustCompile(`gemini-2[.]5-pro`), "google:gemini-2.5-pro"},
	{regexp.MustCompile(`gemini-2[.]5-flash`), "google:gemini-2.5-flash"},
}

func PriceForModelAlias(model string) (ModelPrice, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, rule := range modelAliasRules {
		if rule.re.MatchString(model) {
			priceKey := rule.priceKey
			if strings.Contains(priceKey, "$") {
				priceKey = rule.re.ReplaceAllString(model, priceKey)
			}
			price, ok := modelPrices[priceKey]
			return price, ok
		}
	}
	return ModelPrice{}, false
}

func PriceForProviderModel(provider, model string) (ModelPrice, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if provider != "" && model != "" {
		if price, ok := PriceForModelAlias(provider + "/" + model); ok {
			return price, ok
		}
	}
	return PriceForModelAlias(model)
}

func EstimateUsageCostBreakdownUSD(provider, model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) (UsageCostBreakdown, bool) {
	price, ok := PriceForProviderModel(provider, model)
	if !ok {
		return UsageCostBreakdown{}, false
	}
	if isCodeBuddyUsage(provider, model) {
		return estimateCodeBuddyUsageCostBreakdownUSD(price, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens), true
	}
	breakdown := UsageCostBreakdown{
		InputCostUSD:      RoundCostUSD(tokenCostUSD(inputTokens, price.InputPerM)),
		OutputCostUSD:     RoundCostUSD(tokenCostUSD(outputTokens, price.OutputPerM)),
		CacheReadCostUSD:  RoundCostUSD(tokenCostUSD(cacheReadTokens, price.CacheReadPerM)),
		CacheWriteCostUSD: RoundCostUSD(tokenCostUSD(cacheWriteTokens, price.CacheWritePerM)),
		CacheSavingsUSD:   RoundCostUSD(tokenCostUSD(cacheReadTokens, price.InputPerM-price.CacheReadPerM)),
	}
	breakdown.TotalCostUSD = RoundCostUSD(breakdown.InputCostUSD + breakdown.OutputCostUSD + breakdown.CacheReadCostUSD + breakdown.CacheWriteCostUSD)
	return breakdown, true
}

func EstimateUsageCostUSD(modelAlias string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) (float64, bool) {
	price, ok := PriceForModelAlias(modelAlias)
	if !ok {
		return 0, false
	}
	if isCodeBuddyUsage("", modelAlias) {
		breakdown := estimateCodeBuddyUsageCostBreakdownUSD(price, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
		return breakdown.TotalCostUSD, true
	}
	breakdown := UsageCostBreakdown{
		InputCostUSD:      RoundCostUSD(tokenCostUSD(inputTokens, price.InputPerM)),
		OutputCostUSD:     RoundCostUSD(tokenCostUSD(outputTokens, price.OutputPerM)),
		CacheReadCostUSD:  RoundCostUSD(tokenCostUSD(cacheReadTokens, price.CacheReadPerM)),
		CacheWriteCostUSD: RoundCostUSD(tokenCostUSD(cacheWriteTokens, price.CacheWritePerM)),
	}
	return RoundCostUSD(breakdown.InputCostUSD + breakdown.OutputCostUSD + breakdown.CacheReadCostUSD + breakdown.CacheWriteCostUSD), true
}

func CanonicalModelPriceKey(modelAlias string) (provider, model string, ok bool) {
	price, ok := PriceForModelAlias(modelAlias)
	if !ok {
		return "", "", false
	}
	return price.Provider, price.Model, true
}

func RoundCostUSD(cost float64) float64 {
	return math.Round(cost*1_000_000) / 1_000_000
}

func tokenCostUSD(tokens int64, pricePerM float64) float64 {
	if tokens <= 0 || pricePerM <= 0 {
		return 0
	}
	return float64(tokens) * pricePerM / 1_000_000
}

func isCodeBuddyUsage(provider, model string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	return provider == "codebuddy" || strings.HasPrefix(model, "codebuddy/")
}

func estimateCodeBuddyUsageCostBreakdownUSD(price ModelPrice, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) UsageCostBreakdown {
	inputTokens = codeBuddyEffectiveInputTokens(inputTokens, cacheReadTokens, cacheWriteTokens)
	uncachedInputTokens := inputTokens - cacheReadTokens
	if uncachedInputTokens < 0 {
		uncachedInputTokens = 0
	}
	breakdown := UsageCostBreakdown{
		InputCostUSD:      RoundCostUSD(tokenCostUSD(uncachedInputTokens, price.InputPerM)),
		OutputCostUSD:     RoundCostUSD(tokenCostUSD(outputTokens, price.OutputPerM)),
		CacheReadCostUSD:  RoundCostUSD(tokenCostUSD(cacheReadTokens, price.CacheReadPerM)),
		CacheWriteCostUSD: 0,
		CacheSavingsUSD:   RoundCostUSD(tokenCostUSD(cacheReadTokens, price.InputPerM-price.CacheReadPerM)),
	}
	breakdown.TotalCostUSD = RoundCostUSD(breakdown.InputCostUSD + breakdown.OutputCostUSD + breakdown.CacheReadCostUSD)
	return breakdown
}

func codeBuddyEffectiveInputTokens(inputTokens, cacheReadTokens, cacheWriteTokens int64) int64 {
	if inputTokens < cacheReadTokens+cacheWriteTokens {
		return inputTokens + cacheReadTokens + cacheWriteTokens
	}
	return inputTokens
}
