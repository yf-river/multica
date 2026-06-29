export const SOP_STAGE_DEFINITIONS = [
  { key: "pm", label: "PM", names: ["pm"] },
  { key: "01", label: "01-需求澄清", names: ["01", "01-clarify", "clarify", "01-需求澄清", "需求澄清"] },
  { key: "02", label: "02-方案设计", names: ["02", "02-design", "design", "02-方案设计", "方案设计"] },
  { key: "03", label: "03-任务拆分", names: ["03", "03-task-split", "task-split", "split", "03-任务拆分", "任务拆分"] },
  { key: "04", label: "04-开发", names: ["04", "04-implement", "implement", "04-开发", "开发"] },
  { key: "05", label: "05-测试", names: ["05", "05-verify", "verify", "05-验证", "验证", "05-测试", "测试"] },
] as const;

export function normalizeSopStageName(value: string | undefined) {
  return (value ?? "")
    .toLowerCase()
    .replace(/[_\s]+/g, "-")
    .replace(/^0([1-5])$/, "0$1")
    .trim();
}

export function sopStageDisplayName(value: string | undefined) {
  const normalized = normalizeSopStageName(value);
  if (!normalized) return value ?? "";
  const stage = SOP_STAGE_DEFINITIONS.find((candidate) =>
    candidate.names.some((name) => normalizeSopStageName(name) === normalized),
  );
  return stage?.label ?? value ?? "";
}
