// 本文件负责把 Stripe 以「最小货币单位」记录的金额格式化成用户可读的货币字符串。

/** 零小数位货币：Stripe 里这些币种直接以整数单位（如日元）计价，没有小数点。 */
const STRIPE_ZERO_DECIMAL_CURRENCIES = new Set([
  "BIF",
  "CLP",
  "DJF",
  "GNF",
  "JPY",
  "KMF",
  "KRW",
  "MGA",
  "PYG",
  "RWF",
  "VND",
  "VUV",
  "XAF",
  "XOF",
  "XPF",
]);
/** 两位小数但属于「兼容」类的币种，历史上 Stripe 对它们做过特殊处理，这里统一按两位小数展示。 */
const STRIPE_TWO_DECIMAL_COMPAT_CURRENCIES = new Set(["ISK", "UGX"]);
/** 三位小数币种：如科威特第纳尔等，需要精确到千分位。 */
const STRIPE_THREE_DECIMAL_CURRENCIES = new Set([
  "BHD",
  "JOD",
  "KWD",
  "OMR",
  "TND",
]);

/**
 * 把 Stripe 的「最小货币单位」金额（如 1234 美分）格式化成带货币符号的本地化字符串。
 * @param amount - 最小单位的金额，必须是安全整数且非负。
 * @param currency - 三位货币代码，如 "USD"、"JPY"。
 * @param locale - 本地化区域代码，如 "en-US"。
 * @returns 格式化后的货币字符串；金额不合法或格式化失败时返回 null。
 */
export function formatStripeMinorAmount(
  amount: number,
  currency: string,
  locale: string,
): string | null {
  if (!Number.isSafeInteger(amount) || amount < 0) return null;
  const normalizedCurrency = currency.trim().toUpperCase();
  if (!normalizedCurrency) return null;

  try {
    // 根据币种类型确定小数位数：兼容类币种按 2 位，零小数位按 0 位，三位小数按 3 位，其余默认 2 位。
    const fractionDigits = STRIPE_TWO_DECIMAL_COMPAT_CURRENCIES.has(
      normalizedCurrency,
    )
      ? 2
      : STRIPE_ZERO_DECIMAL_CURRENCIES.has(normalizedCurrency)
        ? 0
        : STRIPE_THREE_DECIMAL_CURRENCIES.has(normalizedCurrency)
          ? 3
          : 2;
    // 把最小单位换算回主单位金额（如 1234 分 -> 12.34 元）。
    const majorAmount = amount / 10 ** fractionDigits;
    // 只有当主单位金额不是整数时才强制展示小数位，避免整数金额被显示成 "12.00"。
    const showStripeFraction = !Number.isInteger(majorAmount);
    return new Intl.NumberFormat(locale, {
      style: "currency",
      currency: normalizedCurrency,
      ...(showStripeFraction
        ? {
            minimumFractionDigits: fractionDigits,
            maximumFractionDigits: fractionDigits,
          }
        : {}),
    }).format(majorAmount);
  } catch {
    return null;
  }
}
