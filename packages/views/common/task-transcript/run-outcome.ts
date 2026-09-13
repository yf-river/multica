// 运行结果聚合：从一次运行的所有调用步骤里，汇总出"它实际做了什么"的高层摘要——
// 改动了哪些文件、增删了多少行、执行了几条命令。回答"它做了什么、可信吗"只需看
// 这张表，不必逐条读步骤。

import { traceEventDetail } from "./trace-event-presenter";
import type { TraceStep } from "./build-steps";

/**
 * 一次运行产出的聚合结果。刻意只汇总已持久化数据能支撑的指标：
 * 不数"评论数"（只能靠猜命令文本来蒙），也不判命令"过/没过"（流里没有退出码，
 * 判不了）。
 */
export interface RunOutcome {
  /** 触碰过的不同文件路径，按首次触碰顺序排列。 */
  paths: string[];
  /** 净新增行数。 */
  addedLines: number;
  /** 净删除行数。 */
  removedLines: number;
  /** 执行的命令条数。 */
  commandCount: number;
}

// 一个结果是否"空"：既没碰任何文件又没跑任何命令。空结果对用户没有展示价值。
function isEmpty(outcome: RunOutcome): boolean {
  return outcome.paths.length === 0 && outcome.commandCount === 0;
}

/**
 * 从执行步骤里聚合出运行结果摘要。
 * @param steps 一次运行的全部跟踪步骤（见 build-steps）。
 * @returns 聚合结果；若这次运行既没碰文件也没跑命令，返回 null——"0 文件 · 0 命令"
 *          的展示会让人误读成"它什么都没干"。
 */
export function buildRunOutcome(steps: TraceStep[]): RunOutcome | null {
  const paths: string[] = [];
  const seen = new Set<string>();
  let addedLines = 0;
  let removedLines = 0;
  let commandCount = 0;

  // 记录一个被触碰的路径，去重且保持首次出现顺序。
  const touch = (path: string) => {
    if (path.length === 0 || seen.has(path)) return;
    seen.add(path);
    paths.push(path);
  };

  for (const step of steps) {
    if (step.kind !== "call" || !step.call) continue;
    const detail = traceEventDetail(step.call);

    switch (detail.kind) {
      case "diff": {
        // 单文件 diff：记录路径，并按行增删类型累计行数。
        touch(detail.path);
        for (const line of detail.lines) {
          if (line.kind === "add") addedLines += 1;
          else if (line.kind === "remove") removedLines += 1;
        }
        break;
      }
      case "file": {
        // 整文件写入：新增行数等于文件总行数。
        touch(detail.path);
        addedLines += detail.lineCount;
        break;
      }
      case "patch": {
        // 一次补丁可能含多个文件，逐个统计。
        for (const file of detail.files) {
          touch(file.path);
          if (file.body.kind === "diff") {
            for (const line of file.body.lines) {
              if (line.kind === "add") addedLines += 1;
              else if (line.kind === "remove") removedLines += 1;
            }
          } else if (file.body.kind === "file") {
            addedLines += file.body.lineCount;
          }
        }
        break;
      }
      case "text": {
        // 判断是不是 shell 命令看入参里的 command 字段，而不是工具名：各家后端
        // 给 shell 起的名字五花八门（Bash、shell、run_command、terminal），但都
        // 会把命令放在 `command` 字符串里传进来。
        if (typeof step.call.input?.command === "string") commandCount += 1;
        break;
      }
    }
  }

  const outcome = { paths, addedLines, removedLines, commandCount };
  // 只读过文件、只发过评论的运行，这一行拿不出任何可命名的东西；硬显示
  // "0 文件 · 0 命令"读起来就像"它没干活"。
  return isEmpty(outcome) ? null : outcome;
}
