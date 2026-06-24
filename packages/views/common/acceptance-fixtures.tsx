import { Button } from "@multica/ui/components/ui/button";

const SAFE_INTERNAL_NAMES = new Set([
  "user-center 小队",
  "用户中心小队",
  "Multica 编码小队",
  "Multica 训练评估智能体",
  "用户中心需求澄清提示词",
]);

const FIXTURE_PATTERNS = [
  /\bcurl\b/i,
  /\be2e\b/i,
  /\bgoal-test\b/i,
  /\bsmoke(?:\s|-)?test\b/i,
  /端到端验收/,
  /真实端到端/,
  /真实.*Agent/,
  /真实.*智能体/,
  /训练闭环/,
  /生产部署验收/,
  /生产验收/,
  /页面验收/,
  /验收创建/,
  /验收小队/,
  /验收智能体/,
  /验收\s*Agent/i,
  /Codex\s*验收/i,
];

function stringifyFixtureValue(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (Array.isArray(value)) return value.map(stringifyFixtureValue).join(" ");
  try {
    return JSON.stringify(value);
  } catch {
    return "";
  }
}

export function isAcceptanceFixtureText(...values: unknown[]): boolean {
  const text = values.map(stringifyFixtureValue).join(" ").trim();
  if (!text) return false;
  return FIXTURE_PATTERNS.some((pattern) => pattern.test(text));
}

export function isNamedInternalProductionSeed(name: unknown): boolean {
  return typeof name === "string" && SAFE_INTERNAL_NAMES.has(name.trim());
}

export function isAcceptanceFixtureRecord<T extends Record<string, unknown>>(
  record: T,
  fields: Array<keyof T>,
): boolean {
  if (isNamedInternalProductionSeed(record.name)) return false;
  return isAcceptanceFixtureText(...fields.map((field) => record[field]));
}

export function AcceptanceFixtureNotice({
  count,
  noun,
  showing,
  onShow,
  onHide,
}: {
  count: number;
  noun: string;
  showing: boolean;
  onShow: () => void;
  onHide: () => void;
}) {
  if (count <= 0) return null;
  return (
    <div
      className="mx-3 flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-950 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-100"
      data-testid="acceptance-fixture-notice"
    >
      <span>
        {showing
          ? `正在显示 ${count} 个验收${noun}，这些数据用于端到端证据复核。`
          : `已隐藏 ${count} 个验收${noun}，避免开发联调数据淹没日常列表。`}
      </span>
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="h-7 bg-background px-2 text-xs"
        onClick={showing ? onHide : onShow}
      >
        {showing ? "隐藏验收数据" : "显示验收数据"}
      </Button>
    </div>
  );
}
