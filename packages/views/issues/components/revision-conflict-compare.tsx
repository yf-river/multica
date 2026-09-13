// 本文件：RevisionConflictCompare —— 问题"修订冲突"的逐行对比面板。
// 当本地未保存的编辑与服务端已落库的新版本冲突时，把两份文本并排成
// 左右两栏 diff（左 server、右 local），每侧各带一个动作
// （如"保留我的版本"）。共享同一行仅作参考。

import type { ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";
import { diffTraceLines } from "../../common/task-transcript/trace-event-presenter";

interface RevisionConflictCompareProps {
  title: string;
  serverLabel: string;
  localLabel: string;
  serverValue: string;
  localValue: string;
  footer?: ReactNode;
  // 每侧一个动作，渲染在与标签和 diff 相同的双列网格里各占一列。
  // 曾经把它们放进单一共享动作行，结果"保留我的版本"看起来像是坐落在
  // 它即将丢弃的 SERVER 预览下方（#7624）；一列一个动作才能让每个动作
  // 指向自己的那一侧，且因为 diff 本身永不堆叠，在任何宽度都成立。
  serverAction?: ReactNode;
  localAction?: ReactNode;
  className?: string;
}

interface RevisionConflictRow {
  server?: { text: string; changed: boolean };
  local?: { text: string; changed: boolean };
}

const textLines = (value: string): string[] =>
  value.length > 0 ? value.split("\n") : [""];

/**
 * 把两份文本跑一次行级 diff，产出左右对齐的行序列（含 changed 标记）。
 */
export function revisionConflictRows(
  serverValue: string,
  localValue: string,
): RevisionConflictRow[] {
  const diff = diffTraceLines(textLines(serverValue), textLines(localValue));
  const rows: RevisionConflictRow[] = [];
  let removed: string[] = [];
  let added: string[] = [];

  const flushChanges = () => {
    const count = Math.max(removed.length, added.length);
    for (let index = 0; index < count; index++) {
      const server = removed[index];
      const local = added[index];
      rows.push({
        server:
          server === undefined ? undefined : { text: server, changed: true },
        local:
          local === undefined ? undefined : { text: local, changed: true },
      });
    }
    removed = [];
    added = [];
  };

  for (const line of diff) {
    if (line.kind === "remove") {
      removed.push(line.text);
    } else if (line.kind === "add") {
      added.push(line.text);
    } else if (line.kind === "context") {
      flushChanges();
      rows.push({
        server: { text: line.text, changed: false },
        local: { text: line.text, changed: false },
      });
    }
  }
  flushChanges();
  return rows;
}

/**
 * 单侧单格的 diff 单元格（增/删/上下文染色）。
 */
function DiffCell({
  side,
  line,
}: {
  side: "server" | "local";
  line?: { text: string; changed: boolean };
}) {
  const changed = line?.changed === true;
  const marker = changed ? (side === "server" ? "−" : "+") : "";
  return (
    <div
      data-diff-kind={
        changed ? (side === "server" ? "remove" : "add") : "context"
      }
      className={cn(
        "grid min-w-0 grid-cols-[1.25rem_minmax(0,1fr)] px-2 py-1 font-mono text-micro leading-relaxed",
        !line && "bg-muted/20",
        changed && side === "server" && "bg-destructive/10 text-destructive",
        changed && side === "local" && "bg-success/10 text-success",
      )}
    >
      <span className="select-none text-center opacity-70">
        {marker}
      </span>
      <span className="min-w-0 whitespace-pre-wrap break-words">
        {line ? line.text || " " : ""}
      </span>
    </div>
  );
}

/**
 * 修订冲突对比面板（职责见文件头）。
 */
export function RevisionConflictCompare({
  title,
  serverLabel,
  localLabel,
  serverValue,
  localValue,
  footer,
  serverAction,
  localAction,
  className,
}: RevisionConflictCompareProps) {
  const rows = revisionConflictRows(serverValue, localValue);
  return (
    <div
      role="alert"
      className={cn(
        "rounded-md border border-warning/40 bg-warning/5 p-3 text-caption",
        className,
      )}
    >
      <div className="font-medium">{title}</div>
      <div className="mt-2 overflow-hidden rounded-md border bg-background/70">
        <div className="grid grid-cols-2 border-b bg-muted/40 font-medium text-muted-foreground">
          <div className="min-w-0 px-3 py-1.5">{serverLabel}</div>
          <div className="min-w-0 border-l px-3 py-1.5">{localLabel}</div>
        </div>
        <div
          data-revision-diff-scroll
          className="max-h-80 overflow-y-auto overscroll-contain"
        >
          {rows.map((row, index) => (
            <div
              key={index}
              className="grid grid-cols-2 border-b last:border-b-0"
            >
              <DiffCell side="server" line={row.server} />
              <div className="min-w-0 border-l">
                <DiffCell side="local" line={row.local} />
              </div>
            </div>
          ))}
        </div>
        {serverAction || localAction ? (
          <div
            data-revision-conflict-actions
            className="grid grid-cols-2 border-t bg-muted/40"
          >
            {/* px-3 与上方的标签格对齐，让每个按钮的左边缘
                落在其列头的同一条垂直线上。 */}
            <div className="min-w-0 px-3 py-2">{serverAction}</div>
            <div className="min-w-0 border-l px-3 py-2">{localAction}</div>
          </div>
        ) : null}
      </div>
      {footer ? (
        <div className="mt-2 text-muted-foreground">{footer}</div>
      ) : null}
    </div>
  );
}
