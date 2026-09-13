"use client";

// 本文件：TerminateTaskConfirmDialog —— 终止单个代理任务的共用确认框。
// 两个会终止任务的详情页入口共用它——头部"代理工作中"chip 浮层
//（IssueAgentHeaderChip）与执行日志区（ExecutionLogSection）的行内动作。
// 任务取消不可逆，而长时运行上的一次误点代价很高，所以两个入口都先过
// 这个对话框、而不是在第一次点击就发出取消请求。
//
// 对话框完全由调用方控制（调用方本来就同时持有 confirmCancel 状态与在途的
// cancelling 状态）。当调用方表明正在终止时，动作按钮呈禁用态，
// 防止第二次点击与在途请求竞态。

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

// 两个会终止单个代理任务的 issue 详情入口共用的确认步骤——头部实时 chip
// 浮层（IssueAgentHeaderChip）与 ExecutionLogSection 里的行内动作。
// 任务取消不可逆、长时运行上的一次误点代价高昂，因此两个入口都经由此
// 对话框，而不是在第一次点击时就发出取消请求。
//
// 对话框由调用方完全控制（调用方已持有 confirmCancel 状态与在途的
// cancelling 状态）。当调用方表示"正在终止"时，动作按钮进入禁用态，
// 让第二次点击无法与在途请求赛跑。
interface TerminateTaskConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  // 运行中的任务在 daemon 侧可能需要几秒才能完全停下；
  // 排队的任务会立即取消，因此省略该提示、保持文案如实。
  showRunningNote?: boolean;
}

/**
 * 终止代理任务的确认对话框（完全受控）。
 * `open` 为 true 才渲染；点"确认"时先关闭自身再触发 `onConfirm`。
 * 对话框若渲染在可点击祖先（如执行日志行）内，点击内部会 stopPropagation。
 */
export function TerminateTaskConfirmDialog({
  open,
  onOpenChange,
  onConfirm,
  showRunningNote = false,
}: TerminateTaskConfirmDialogProps) {
  const { t } = useT("issues");

  if (!open) return null;

  return (
    <AlertDialog open onOpenChange={onOpenChange}>
      <AlertDialogContent
        // 拦截对话框内部的点击向行/横幅（下方可点击祖先，如执行日志行）冒泡。
        onClick={(e) => e.stopPropagation()}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.terminate_dialog.title)}</AlertDialogTitle>
          <AlertDialogDescription>
            {t(($) => $.terminate_dialog.body)}
            {showRunningNote && t(($) => $.terminate_dialog.running_note)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t(($) => $.terminate_dialog.keep)}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              onOpenChange(false);
              onConfirm();
            }}
          >
            {t(($) => $.terminate_dialog.confirm)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
