import type { TimelineItem } from "./build-timeline";

const TOOL_LABELS: Record<string, string> = {
  bash: "终端",
  shell: "终端",
  terminal: "终端",
  exec_command: "终端",
  taskcreate: "创建待办",
  task_create: "创建待办",
  taskupdate: "更新待办",
  task_update: "更新待办",
  todowrite: "更新待办",
  todo_write: "更新待办",
  grep: "搜索",
  glob: "搜索文件",
  read: "读取",
  write: "写入",
  edit: "编辑",
  multiedit: "批量编辑",
  multi_edit: "批量编辑",
  ls: "列目录",
};

const EVENT_LABELS: Record<TimelineItem["type"], string> = {
  text: "智能体",
  thinking: "思考",
  tool_use: "工具",
  tool_result: "结果",
  error: "错误",
};

const TOOL_OUTPUT_PREFIXES: Array<[RegExp, string]> = [
  [/(^|\n)Command:\s?/g, "$1命令："],
  [/(^|\n)Stdout:\s?/g, "$1标准输出："],
  [/(^|\n)Stderr:\s?/g, "$1标准错误："],
  [/(^|\n)Exit Code:\s?/g, "$1退出码："],
  [/(^|\n)Signal:\s?/g, "$1信号："],
];

function normalizeToolName(tool: string): string {
  return tool.trim().replace(/[\s-]+/g, "_").toLowerCase();
}

export function formatToolName(tool?: string): string {
  if (!tool) return "";
  const trimmed = tool.trim();
  if (!trimmed) return "";
  return TOOL_LABELS[normalizeToolName(trimmed)] ?? trimmed;
}

export function formatEventLabel(item: TimelineItem): string {
  if (item.type === "tool_use") return formatToolName(item.tool) || EVENT_LABELS.tool_use;
  if (item.type === "tool_result") return formatToolName(item.tool) || EVENT_LABELS.tool_result;
  return EVENT_LABELS[item.type] ?? "事件";
}

export function formatFilterLabel(item: TimelineItem): string {
  if (item.tool && (item.type === "tool_use" || item.type === "tool_result")) {
    return `工具：${formatToolName(item.tool)}`;
  }
  return formatEventLabel(item);
}

export function localizeTranscriptOutput(value: string): string {
  let out = value;
  for (const [pattern, replacement] of TOOL_OUTPUT_PREFIXES) {
    out = out.replace(pattern, replacement);
  }
  return out
    .replaceAll("(empty)", "（空）")
    .replaceAll("... (truncated)", "...（已截断）");
}

export function truncateTranscriptText(value: string, maxLength: number): string {
  if (value.length <= maxLength) return value;
  return `${value.slice(0, maxLength)}...`;
}

export function transcriptTruncatedSuffix(): string {
  return "\n...（已截断）";
}
