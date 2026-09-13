// 本文件：TaskStatusIcon —— 单次代理运行（AgentTask）的终态图标。
// 由执行日志行与用量详情对话框共用，让"失败的运行"无论出现在哪都带同一记号。
// 非终态状态不渲染任何图形：那些行挂着实时计时器或状态文字，
// 旁边再放个图标会读成第二个、且相互矛盾的状态。

import { Ban, CheckCircle2, XCircle } from "lucide-react";
import type { AgentTask } from "@multica/core/types";

/**
 * 代理运行终态图标：completed→对勾（成功色）、failed→叉（危险色）、
 * cancelled→禁用斜杠（弱化色）；其余进行中/排队等状态返回 null（不渲染）。
 */
export function TaskStatusIcon({ status }: { status: AgentTask["status"] }) {
  switch (status) {
    case "completed":
      return <CheckCircle2 aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-success" />;
    case "failed":
      return <XCircle aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-destructive" />;
    case "cancelled":
      return <Ban aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />;
    default:
      return null;
  }
}
