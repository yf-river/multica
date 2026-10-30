// @deepseek-ai/dsh-client-ui-chat 0.2.1-rc.3, lib/client.js token-format; MIT, see LICENSE.
function roundedPercentUnits(cacheReadTokens, denominator, decimalPlaces) {
	const scale = (decimalPlaces === 0 ? 1 : 10) * 100;
	const doubledScale = scale * 2;
	const denominatorQuotient = Math.floor(denominator / doubledScale);
	const denominatorRemainder = denominator % doubledScale;
	let lower = 0;
	let upper = scale;
	while (lower < upper) {
		const candidate = Math.floor((lower + upper + 1) / 2);
		const factor = candidate * 2 - 1;
		if (cacheReadTokens >= factor * denominatorQuotient + Math.ceil(factor * denominatorRemainder / doubledScale)) lower = candidate;
		else upper = candidate - 1;
	}
	return lower;
}
function displayPercentUnits(units, decimalPlaces) {
	if (decimalPlaces === 0) return String(units);
	const whole = Math.floor(units / 10);
	const tenths = units % 10;
	return tenths === 0 ? String(whole) : `${whole}.${tenths}`;
}
/**
* Display-ready cache-hit share without rounding a partial hit to 100%.
* @param cacheReadTokens - exact prompt tokens served from cache.
* @param promptTokens - exact aggregate prompt tokens.
* @param decimalPlaces - ordinary-ratio precision; partial hits that would
* round to 100 automatically use enough additional precision to stay honest.
* @returns percentage text, or null when there was no prompt input.
*/
export function formatCacheHitPercent(cacheReadTokens, promptTokens, decimalPlaces = 0) {
	if (promptTokens === 0) return null;
	const missedInputTokens = promptTokens - cacheReadTokens;
	if (missedInputTokens === 0) return "100";
	const roundedUnits = roundedPercentUnits(cacheReadTokens, promptTokens, decimalPlaces);
	if (roundedUnits < (decimalPlaces === 0 ? 100 : 1e3)) return displayPercentUnits(roundedUnits, decimalPlaces);
	let distinguishingPlaces = 1;
	let scaledDoubleGap = missedInputTokens * 200;
	const denominatorTens = Math.floor(promptTokens / 10);
	while (scaledDoubleGap <= denominatorTens) {
		scaledDoubleGap *= 10;
		distinguishingPlaces += 1;
	}
	const denominatorOnes = promptTokens % 10;
	let roundedLoss = 5;
	for (let loss = 1; loss < 5; loss += 1) {
		const factor = loss * 2 + 1;
		const threshold = factor * denominatorTens + Math.floor(factor * denominatorOnes / 10);
		if (scaledDoubleGap <= threshold) {
			roundedLoss = loss;
			break;
		}
	}
	return `99.${"9".repeat(distinguishingPlaces - 1)}${10 - roundedLoss}`;
}
