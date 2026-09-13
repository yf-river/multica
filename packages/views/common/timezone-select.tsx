// 时区下拉选择与浏览器时区探测：所有需要让用户选时区的地方共用的 Select 控件，
// 以及"浏览器当前时区叫什么"的探测函数。列表数据源统一（当前值、浏览器时区、
// 常用时区、运行时可枚举的全部时区去重合并），避免各处复制导致列表漂移。

"use client";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";

/**
 * 精选的常用时区回退列表：当运行时缺少 `Intl.supportedValuesOf` 时用它兜底。
 * 导出是为了让每个时区选择器都从同一份数据源取数，而不是各自维护一份慢慢漂移
 * 的副本。
 */
export const COMMON_TIMEZONES = [
  "UTC",
  "America/Los_Angeles",
  "America/Denver",
  "America/Chicago",
  "America/New_York",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "Europe/Moscow",
  "Africa/Cairo",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Bangkok",
  "Asia/Shanghai",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Pacific/Auckland",
];

// 探测结果做模块级缓存：会话期间浏览器时区不会变，反复探测纯属浪费。
let cachedBrowserTZ: string | null = null;

/**
 * 探测浏览器的当前时区（IANA 名称，如 "Asia/Shanghai"）。
 * @returns 探测到的时区名；探测失败或结果为占位符时回退 "UTC"。
 */
export function browserTimezone(): string {
  if (cachedBrowserTZ !== null) return cachedBrowserTZ;
  try {
    const detected = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (!detected) return (cachedBrowserTZ = "UTC");
    // 某些无头/精简 ICU 构建会报占位符（如 `Etc/Unknown`），而且格式化时反而会
    // 拒绝这个值——所以用 Intl 再验证一遍，能过才算真时区。
    new Intl.DateTimeFormat(undefined, { timeZone: detected });
    cachedBrowserTZ = detected;
  } catch {
    cachedBrowserTZ = "UTC";
  }
  return cachedBrowserTZ;
}

/**
 * 清空模块级 `browserTimezone()` 缓存。浏览器代码永远用不上——同一会话内时区
 * 稳定——但该缓存会跨 Vitest 测试文件存活在同一 worker 里，因此任何 stub 了
 * `Intl.DateTimeFormat` 的测试（直接或经 fake timezone）都必须在 `beforeEach`
 * 里调用它，否则上个套件缓存的值会泄漏进来。整模块 mock 掉
 * `./timezone-select` 的测试不受影响。
 */
export function resetBrowserTimezoneCache(): void {
  cachedBrowserTZ = null;
}

// Intl 的 supportedValuesOf 是较新的能力，旧运行时没有，这里做可选探测的类型。
type IntlWithSupportedValues = typeof Intl & {
  supportedValuesOf?: (key: "timeZone") => string[];
};

// 枚举当前运行时支持的全部 IANA 时区；运行时不支持 supportedValuesOf 或返回空
// 时，退回到 COMMON_TIMEZONES 精选列表。
function supportedTimezones(): string[] {
  try {
    const supported = (Intl as IntlWithSupportedValues).supportedValuesOf?.(
      "timeZone",
    );
    return supported && supported.length > 0 ? supported : COMMON_TIMEZONES;
  } catch {
    return COMMON_TIMEZONES;
  }
}

/**
 * 汇总时区下拉的可选项：当前值 + 浏览器时区 + 精选列表 + 运行时全部时区，去重、
 * 过滤空值后返回。把当前值和浏览器时区排在最前，保证这两项用户总能快速选中。
 * @param current 当前选中的时区，确保它一定出现在结果里。
 * @returns 去重后的时区列表。
 */
export function timezoneOptions(current: string): string[] {
  const browser = browserTimezone();
  return Array.from(
    new Set([current, browser, ...COMMON_TIMEZONES, ...supportedTimezones()]),
  ).filter(Boolean);
}

/**
 * 时区下拉选择器。可选项由 {@link timezoneOptions} 汇总；浏览器当前时区会在名称
 * 后拼上 `browserSuffix` 提示用户（如 "Asia/Shanghai · 当前浏览器"）。
 * @param value 当前选中的时区。
 * @param onValueChange 选中变化回调。
 * @param browserSuffix 浏览器时区选项的名称后缀。
 * @param disabled 是否禁用。
 * @param triggerClassName 触发器样式。
 */
export function TimezoneSelect({
  value,
  onValueChange,
  browserSuffix,
  disabled,
  triggerClassName,
}: {
  value: string;
  onValueChange: (next: string) => void;
  browserSuffix: string;
  disabled?: boolean;
  triggerClassName?: string;
}) {
  const browser = browserTimezone();
  const options = timezoneOptions(value);
  const render = (tz: string) =>
    tz === browser ? `${tz}${browserSuffix}` : tz;
  const items = options.map((value) => ({ value, label: render(value) }));

  return (
    <Select
      items={items}
      value={value}
      disabled={disabled}
      onValueChange={(next) => {
        if (next) onValueChange(next);
      }}
    >
      <SelectTrigger
        size="sm"
        className={triggerClassName ?? "w-full rounded-md font-mono text-caption"}
      >
        <SelectValue>{render(value)}</SelectValue>
      </SelectTrigger>
      <SelectContent align="start" className="max-h-72">
        {items.map((item) => (
          <SelectItem key={item.value} value={item.value} className="font-mono text-caption">
            {item.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
