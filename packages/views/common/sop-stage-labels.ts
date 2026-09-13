// SOP 阶段名称的展示标签工具：把各种写法（数字代号、英文 slug、中文名称等）的
// 阶段标识归一化后，匹配到统一的展示名称。SOP 是智能体任务的标准作业流程
// （如"需求澄清 → 方案设计 → 任务拆分 → 开发 → 验证测试"），不同数据源可能用
// 不同写法记录当前处于哪个阶段，本模块负责把它们统一翻译成界面上的中文标签。
//
// 用法：界面拿到一个阶段值（可能是 "01"、"clarify"、"需求澄清" 甚至
// "01-需求澄清"），先 normalizeSopStageName 归一化，再由
// sopStageDisplayName 查表得到唯一的展示名称；查不到就原样返回输入。

// 五个阶段的定义表。每项的 names 列出该阶段在历史数据里可能出现过的所有写法，
// 展示名统一用 label（中文，含编号前缀，如 "01-需求澄清"）。
const SOP_STAGE_DEFINITIONS = [
  { key: "01", label: "01-需求澄清", names: ["01", "01-clarify", "clarify", "01-需求澄清", "需求澄清"] },
  { key: "02", label: "02-方案设计", names: ["02", "02-design", "design", "02-方案设计", "方案设计"] },
  { key: "03", label: "03-任务拆分", names: ["03", "03-task-split", "task-split", "split", "03-任务拆分", "任务拆分"] },
  { key: "04", label: "04-开发", names: ["04", "04-implement", "implement", "04-开发", "开发"] },
  { key: "05", label: "05-验证测试", names: ["05", "05-verify", "verify", "05-验证", "验证", "05-测试", "测试", "05-验证测试", "验证测试"] },
] as const;

/**
 * 归一化 SOP 阶段写法：统一转小写，并把下划线/连续空白折叠成连字符，再去掉
 * 首尾空白。这样 "01_需求澄清"、"01 需求澄清"、"CLARIFY" 等不同历史写法都能
 * 比对到同一个归一化形式。查表比较前必须先过这一层，否则别名里任何大小写或
 * 分隔符差异都会导致匹配失败。
 * @param value 原始阶段值，可能为 undefined。
 * @returns 归一化后的字符串（空输入得到空字符串）。
 */
export function normalizeSopStageName(value: string | undefined) {
  return (value ?? "").toLowerCase().replace(/[_\s]+/g, "-").trim();
}

/**
 * 把任意写法的 SOP 阶段值解析成统一的展示名称（中文标签）。
 * 空值/未知写法时原样返回输入，保证界面不会因为一条旧数据里的怪写法而崩掉。
 * @param value 原始阶段值，可能为 undefined。
 * @returns 匹配到的中文标签；匹配不到时返回原值（undefined 则返回 ""）。
 */
export function sopStageDisplayName(value: string | undefined) {
  const normalized = normalizeSopStageName(value);
  if (!normalized) return value ?? "";
  const stage = SOP_STAGE_DEFINITIONS.find((candidate) =>
    candidate.names.some((name) => normalizeSopStageName(name) === normalized),
  );
  return stage?.label ?? value ?? "";
}
