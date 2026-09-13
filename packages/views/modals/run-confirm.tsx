// 本文件职责：「交接确认」弹窗——把 issue 指派给 agent/小队（单条或批量）、或把某个
// issue 移出 Backlog 触发运行前，向用户确认是否执行；可选附上交给 agent 的交接说明。
"use client";

import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Spinner } from "@multica/ui/components/ui/spinner";
import type { IssueAssigneeType, IssueStatus, UpdateIssueRequest } from "@multica/core/types";
import { useUpdateIssue, useBatchUpdateIssues } from "@multica/core/issues/mutations";
import { errorCode } from "@multica/core/api";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions, squadListOptions } from "@multica/core/workspace/queries";
import { runtimeListOptions, readRuntimeCliVersion, handoffSupported } from "@multica/core/runtimes";
import { useShortcut, shortcutMatchesEvent, isPlainShortcut } from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { useStatusLabel } from "../issues/utils/status-label";
import { useT } from "../i18n";

// 交接说明（handoff note）的最大字符数。
const MAX_HANDOFF_NOTE = 2000;

// i18next 会把 {{name}} / {{status}} 直接内联进句子，但它们在不同语言里的位置不同
// （"{{name}} 会…" vs "Once assigned, {{name}} will…" vs "{{name}}'s leader…"）。
// 给每个占位符围上哨兵符，就能在渲染时只加粗那几段文字，而无需按语言拆成
// 前缀/后缀文案键。加粗同时也在语义上把自定义状态名标记成「状态」而非普通词语
// （"Move this issue to Later."）。
const FENCE = "\u0000";

/** 用哨兵符把一段值包起来，供渲染端识别加粗区间。 */
const fenced = (value: string) => `${FENCE}${value}${FENCE}`;

/**
 * 把被哨兵符包围的片段渲染成加粗的 `<span>`，其余文字原样输出。
 *
 * 每个被围片段恰好贡献一个奇数下标的部分；没有围过的字符串整体是一段，直接按原文返回。
 */
function boldFenced(text: string): ReactNode {
  const parts = text.split(FENCE);
  // 没有被围过的字符串 split 后只有一段（parts.length < 3），整体原样渲染即可。
  if (parts.length < 3) return text;
  return (
    <>
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <span key={i} className="font-semibold text-foreground">
            {part}
          </span>
        ) : (
          part
        ),
      )}
    </>
  );
}

/** 弹窗打开时携带的数据，按 `mode` 区分两种场景。 */
interface RunConfirmData {
  /** 要指派/提升的 issue id 列表（单条为 1 个，批量可多个） */
  issueIds?: string[];
  // 只有两个「把活交给 agent」的 issue 写入需要本弹窗确认。`assign` 给 issue 一个
  // agent/小队负责人；`promote` 把一个已有负责人的 issue 移出 Backlog 类状态——这会
  // 自行触发一次运行（RunSourceStatus）。批量改状态仍然直接生效（MUL-4155）；
  // `promote` 只用于单条 issue 的入口（MUL-6463）。
  mode?: "assign" | "promote";
  /** 仅 promote：issue 要移到的那个状态（键）。 */
  status?: IssueStatus;
  assigneeType?: IssueAssigneeType;
  assigneeId?: string;
  assigneeName?: string;
  issueRevision?: number;
}

/**
 * 针对「会启动 agent 运行」的 issue 写入动作的交接确认弹窗。
 *
 * 铁律是「弹窗 = 你在把活交给 agent」，而不是「你在确认 N 次运行」（MUL-5010）。
 * 因此它不做任何预判式请求：打开时不发任何请求，第一帧即可输入交接说明、点按钮。
 * 早期版本在打开时调用 POST /api/issues/preview-trigger，整个弹窗被卡在一个
 * 「检查中…」转圈后面；而那个查询按 issue id 作 key、staleTime 为 0，所以每个新
 * issue 都必然缓存未命中，等待躲不掉。
 *
 * 完成是「静默」的：负责人/状态的变更、以及由此启动的任何运行，都会通过 issue 的
 * 常规更新冒出来，所以本弹窗不再弹结果 toast；是否启动运行仍是服务端在写入时刻的
 * 既有决策。关闭弹窗（X / Esc / 点击遮罩）等于取消、不产生任何写入。单条指派（1 个
 * id）、批量指派（N 个 id）、以及单条 issue 移出 Backlog 这三种场景共用本弹窗。
 */
export function RunConfirmModal({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const { t: tIssues } = useT("issues");
  const { getActorName } = useActorName();
  const sendShortcut = useShortcut("send");
  const d = (data ?? {}) as RunConfirmData;
  const issueIds = d.issueIds ?? [];

  const [note, setNote] = useState("");
  // 当前正在执行的是哪个底部动作——只有被点的那颗按钮显示转圈（带交接说明的指派会在
  // 服务端跑一个 agent，并非瞬时完成，只禁用不加转圈的观感像卡死）。
  const [pendingAction, setPendingAction] = useState<"go" | "suppress" | null>(null);
  const submitting = pendingAction !== null;

  const updateIssue = useUpdateIssue();
  const batchUpdate = useBatchUpdateIssues();

  // 「是否支持交接说明」的判定，完全由本机热点缓存解析得出（useWorkspacePresencePrefetch
  // 保持 agents / squads / runtimes 处于新鲜状态），所以第一帧就能定案、无需网络往返——
  // 与 quick-create 的版本闸门同构。选 agent 时目标是它自己的 runtime；选小队时目标是
  // 其队长的，小队列表直接给出队长，因此两种情况都能在本地知道。`null` 表示「说不准」
  // （负责人还没进缓存，或没绑 runtime），此时输入框保持可用：交接说明是软闸门，
  // 宁可有条老 daemon 会丢弃的说明，也不该误报一次警告。
  const wsId = useWorkspaceId();
  // 内置状态名走 i18n，自定义状态名走目录查询，保证提升标题与用户刚用过的那个
  // 状态选择器读起来一致。
  const statusLabel = useStatusLabel(wsId);
  const { data: agents = [] } = useQuery({ ...agentListOptions(wsId), enabled: !!wsId });
  const { data: runtimes = [] } = useQuery({ ...runtimeListOptions(wsId), enabled: !!wsId });
  const { data: squads = [] } = useQuery({ ...squadListOptions(wsId), enabled: !!wsId });
  const localHandoff = useMemo<boolean | null>(() => {
    if (!d.assigneeId) return null;
    let agentId: string | undefined;
    if (d.assigneeType === "agent") {
      agentId = d.assigneeId;
    } else if (d.assigneeType === "squad") {
      // 小队的运行由队长执行，所以必须渲染说明的是队长的 runtime。
      agentId = squads.find((s) => s.id === d.assigneeId)?.leader_id;
    }
    if (!agentId) return null;
    const agent = agents.find((a) => a.id === agentId);
    if (!agent?.runtime_id) return null;
    const runtime = runtimes.find((r) => r.id === agent.runtime_id);
    if (!runtime) return null;
    return handoffSupported(readRuntimeCliVersion(runtime.metadata));
  }, [d.assigneeType, d.assigneeId, agents, runtimes, squads]);

  // 软闸门：老 runtime 渲染不了说明。禁用输入框，但指派照常放行（MUL-3375 §6.3）。
  const noteDisabled = localHandoff === false;

  // 提升（promote）只携带状态、别无其他：负责人已经挂在 issue 上，重复提交同一负责人
  // 会在服务端触发谓词那边把「状态写入」变成「负责人写入」。
  const isPromote = d.mode === "promote" && !!d.status;

  const applyTo = (extra: Partial<UpdateIssueRequest>) => {
    const base: UpdateIssueRequest = isPromote
      ? { status: d.status }
      : {
          assignee_type: d.assigneeType ?? null,
          assignee_id: d.assigneeId ?? null,
        };
    return { ...base, ...extra };
  };

  // 文案点名「把 issue 交给谁」；交给小队时点名小队本身——由它的队长决定谁去干活是
  // 内部细节，不必向用户展开。
  const assigneeName =
    d.assigneeName ??
    getActorName(d.assigneeType === "squad" ? "squad" : "agent", d.assigneeId ?? "");

  const submit = async (suppressRun: boolean) => {
    if (issueIds.length === 0 || submitting) return;
    setPendingAction(suppressRun ? "suppress" : "go");
    const payload = applyTo({
      ...(suppressRun ? { suppress_run: true } : {}),
      ...(!suppressRun && !noteDisabled && note.trim() ? { handoff_note: note.trim() } : {}),
    });
    try {
      // 完成后静默，和以前一样：负责人及任何运行都会通过 issue 常规的负责人/运行状态
      // 更新浮出来，所以这里不需要加结果 toast。是否启动了运行，是服务端在写入时刻的
      // 既有决策，不由本弹窗报告。
      if (issueIds.length === 1) {
        await updateIssue.mutateAsync({
          id: issueIds[0]!,
          ...payload,
        });
      } else {
        await batchUpdate.mutateAsync({ ids: issueIds, updates: payload });
      }
      onClose();
    } catch (err) {
      toast.error(
        errorCode(err) === "revision_conflict"
          ? tIssues(($) => $.revision.conflict)
          : err instanceof Error && err.message
            ? err.message
            : t(($) => $.run_confirm.toast_failed),
      );
      setPendingAction(null);
    }
  };

  /**
   * 配置的 `send` 组合键确认本次指派——与 issue 编辑区「用 send 创建」是同一个键
   * （MUL-5694）。
   *
   * 绑在弹窗上而不是说明输入框上，因为该组合键意味着「无论焦点在哪，执行主动作」——
   * 而焦点并不总在输入框里。老 runtime 会禁用它，把初始焦点让给 footer，那正是确认键
   * 上快捷键键帽本该提示的那个键位已经失灵的位置。
   */
  const onDialogKeyDown = (e: React.KeyboardEvent) => {
    // 长按组合键只提交一次；另外，IME 组字过程中用来「确认候选词」的 Enter 绝不是
    // 一次提交确认。
    if (e.defaultPrevented || e.repeat || isImeComposing(e)) return;
    if (!shortcutMatchesEvent(sendShortcut, e.nativeEvent)) return;
    // 只有「裸 Enter」会激活处于聚焦态的按钮（Chromium 对 ⌘/Ctrl+Enter 不派发
    // click），所以 `send` 被重映射成纯 Enter 是唯一需要提防双写的场景——并且在
    // 「先不要启动」上，这两次写入会对 suppress_run 给出矛盾的取值。其余任何组合键
    // 形态到 footer 都是死键，不经过我们，所以必须放行。
    const activatesFocusedButton =
      isPlainShortcut(sendShortcut, "Enter") &&
      e.target instanceof HTMLElement &&
      e.target.closest("button") !== null;
    if (activatesFocusedButton) return;
    e.preventDefault();
    void submit(false);
  };

  // 文案陈述的是「动作本身」而不是预测：写入是一定的，运行是有条件的，所以文案不点名
  // 运行次数。提升（promote）按其工作区标签点名要移入的状态——自定义状态只能靠管理员
  // 起的名字才认得出来。
  const headline: ReactNode = boldFenced(
    isPromote
      ? t(($) => $.run_confirm.promote_single, {
          name: fenced(assigneeName),
          status: fenced(statusLabel(d.status ?? "")),
        })
      : issueIds.length > 1
        ? t(($) => $.run_confirm.assign_batch, {
            name: fenced(assigneeName),
            count: issueIds.length,
          })
        : t(($) => $.run_confirm.assign_single, { name: fenced(assigneeName) }),
  );

  return (
    <Dialog open onOpenChange={(v) => { if (!v && !submitting) onClose(); }}>
      <DialogContent onKeyDown={onDialogKeyDown}>
        <DialogHeader>
          <DialogTitle>
            {isPromote
              ? t(($) => $.run_confirm.title_promote)
              : t(($) => $.run_confirm.title_assign)}
          </DialogTitle>
          <DialogDescription>{headline}</DialogDescription>
        </DialogHeader>

        {/* 始终挂载、第一帧即可输入——这个输入框不依赖任何服务端答复。 */}
        <div className="grid gap-1.5">
          <label className="text-body font-medium" htmlFor="handoff-note">
            {t(($) => $.run_confirm.note_label)}
          </label>
          <Textarea
            id="handoff-note"
            value={note}
            maxLength={MAX_HANDOFF_NOTE}
            disabled={submitting || noteDisabled}
            placeholder={t(($) => $.run_confirm.note_placeholder)}
            onChange={(e) => setNote(e.target.value)}
            rows={3}
          />
          {noteDisabled ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.run_confirm.note_unsupported)}</p>
          ) : null}
        </div>

        {/* 剩下的唯一一个转圈在用户刚按下的那颗按钮上，反映的是「进行中的写入」——
            绝不是某种预检。 */}
        <DialogFooter>
          <Button type="button" variant="outline" disabled={submitting} onClick={() => submit(true)}>
            {pendingAction === "suppress" ? <Spinner className="size-4" /> : t(($) => $.run_confirm.dont_start)}
          </Button>
          <Button type="button" disabled={submitting} onClick={() => submit(false)}>
            {pendingAction === "go" ? (
              <Spinner className="size-4" />
            ) : (
              <>
                {isPromote
                  ? t(($) => $.run_confirm.confirm_promote)
                  : t(($) => $.run_confirm.confirm_assign)}
                {/* 纯装饰：无障碍名称保持按钮自己的文案，而不是「确认指派 Command
                    Enter」。`send` 未绑定时不显示。 */}
                {sendShortcut ? (
                  <ShortcutKeycaps
                    shortcut={sendShortcut}
                    decorative
                    className="ml-1"
                    keyClassName="border-background/30 bg-background/15 text-primary-foreground shadow-none"
                  />
                ) : null}
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
