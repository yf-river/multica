"use client";

// 面向「引导时没填来源」老用户的来源归因弹窗：自己挂载在工作区外壳上，按
// 两层门槛判断是否弹出，把「怎么知道 Multica 的」单选/自定义题补回来。
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Briefcase,
  CalendarDays,
  Globe,
  HelpCircle,
  MoreHorizontal,
  Newspaper,
  Users,
} from "lucide-react";
import { toast } from "sonner";
import { useAuthStore } from "@multica/core/auth";
import {
  agentCompletedIssueCountOptions,
  needsSourceBackfill,
  saveQuestionnaire,
  SOURCE_BACKFILL_MIN_AGENT_DONE_ISSUES,
  type QuestionnaireAnswers,
  type Source,
} from "@multica/core/onboarding";
import { useCurrentWorkspace } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
} from "@multica/ui/components/ui/dialog";
import {
  GitHubIcon,
  GoogleIcon,
  LinkedInIcon,
  OpenAIIcon,
  XIcon,
  YouTubeIcon,
} from "./components/brand-icons";
import {
  IconOptionCard,
  IconOtherOptionCard,
  type QuestionOption,
} from "./components/icon-option-card";
import { mergedQuestionnairePatch } from "./source-backfill-merge";
import { useSourceBackfillDismissCount } from "./source-backfill-dismiss";
import { useT } from "../i18n";

// 本弹窗只会改动这三个来源字段；其余问卷字段靠 mergedQuestionnairePatch 保留。
const EMPTY_BACKFILL: Pick<
  QuestionnaireAnswers,
  "source" | "source_other" | "source_skipped"
> = {
  source: [],
  source_other: null,
  source_skipped: false,
};

/**
 * 给「问卷里从没记过来源」的已引导用户的来源归因弹窗。引导流程本身不问你来自
 * 哪里——这个弹窗是唯一的采集点。以 Dialog 遮罩叠在工作区外壳之上，用户仍能隔着
 * 变暗的背景看到自己的工作区上下文。
 *
 * 自己挂载：调用方只需在 dashboard 布局里放一次 `<SourceBackfillModal />`。
 * 由两层门槛决定是否弹出：
 *
 *   1. 用户级：`needsSourceBackfill(user, dismissCount)`——没记过来源、
 *      从没明确拒绝过、关闭次数未达上限。
 *   2. 工作区级：这里的 Agent（或 squad）已完成至少
 *      SOURCE_BACKFILL_MIN_AGENT_DONE_ISSUES 条任务。归因对用户是零回报的请求，
 *      所以要等 Multica 已经明显交付了价值再问。计数查询只在门槛 1 通过时才跑，
 *      已稳定的用户永远不为它买单。
 *
 * 一旦弹窗打开，就把「这次打开」的决定记进 ref，这样后续把判定条件翻成 false 的
 * 重渲染（例如提交后、refreshMe 还没往返前）不会在半路把弹窗拆掉。
 *
 * 三种退出形态：
 *   - 提交        → PATCH 合并后的问卷，终结态。
 *   - 跳过        → PATCH `source_skipped=true`，终结态（从此不再问）。
 *   - 关闭 X / ESC → 该用户的关闭计数 +1；计数到上限后下次挂载判定为 false。
 *
 * 状态存放是有意区分的：
 *   - source / source_other：服务端（JSONB，与之前答案合并）。
 *   - dismissCount：每用户 localStorage，仅视图层。
 */
export function SourceBackfillModal() {
  const user = useAuthStore((s) => s.user);
  const userId = user?.id ?? null;
  const [dismissCount, bumpDismissCount] =
    useSourceBackfillDismissCount(userId);

  // 先查门槛 1：它纯计算、零成本。门槛 2 要花一次（很小的）任务数查询，所以只在
  // 门槛 1 通过、且已知道要在哪个工作区里数时才启用。
  const userEligible = needsSourceBackfill(user, dismissCount);
  const wsId = useCurrentWorkspace()?.id ?? null;
  const { data: agentDoneCount } = useQuery({
    ...agentCompletedIssueCountOptions(wsId ?? ""),
    enabled: userEligible && wsId !== null,
  });
  const shouldPrompt =
    userEligible &&
    (agentDoneCount ?? 0) >= SOURCE_BACKFILL_MIN_AGENT_DONE_ISSUES;

  // 对每个 (user, 门槛变化) 只决策一次「弹窗要不要开」。打开之后就不再回头重算
  // 判定，这样提交动画还没跑完时、中途 refreshMe（会写入 source）不会把弹窗
  // 卸载掉。`openedRef` 在用户身份变化时重置。
  //
  // Strict-mode 注意：这个 effect 在开发模式下会跑两遍。ref 必须在弹窗真正打开
  // 那一刻才盖章——而不是在 effect 调度定时器时就盖——否则第二遍 strict-mode
  // 看到 `openedForUserRef.current === user.id` 就直接退出了，而它清理掉的定时器
  // 又没人补，弹窗就永远关着。
  const [open, setOpen] = useState(false);
  const openedForUserRef = useRef<string | null>(null);
  useEffect(() => {
    if (!user) {
      openedForUserRef.current = null;
      setOpen(false);
      return;
    }
    if (openedForUserRef.current === user.id) return;
    if (!shouldPrompt) return;
    // 温和进入：先让用户看到工作区一拍，再让弹窗飘进来，不至于像被硬拦一道。
    // 常见的讨喜做法——约 700ms 短到没人会先发起交互，又长到工作区已经渲染完。
    // 尊重 `prefers-reduced-motion: reduce`：这类用户已选择不看不必要的动画，
    // 那就立刻打开。
    const reducedMotion =
      typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
    if (reducedMotion) {
      openedForUserRef.current = user.id;
      setOpen(true);
      return;
    }
    const uid = user.id;
    const timer = window.setTimeout(() => {
      openedForUserRef.current = uid;
      setOpen(true);
    }, 700);
    return () => window.clearTimeout(timer);
  }, [user, shouldPrompt]);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next || !open) return;
        // Base UI 对点 X、按 ESC、点遮罩都会触发 onOpenChange(false)——这三种
        // 都算「未提交就关闭」。
        bumpDismissCount();
        setOpen(false);
      }}
    >
      <SourceBackfillDialogBody
        onComplete={() => setOpen(false)}
      />
    </Dialog>
  );
}

SourceBackfillModal.displayName = "SourceBackfillModal";

/**
 * 弹窗内层内容抽成独立组件，是为了让昂贵的初始化（选项列表、effects）只在弹窗
 * 真正打开时运行。关闭弹窗会卸载这棵子树、清空选择状态——下次打开是全新状态。
 */
function SourceBackfillDialogBody({
  onComplete,
}: {
  onComplete: () => void;
}) {
  const { t } = useT("onboarding");

  const [answers, setAnswers] = useState(EMPTY_BACKFILL);
  const [busy, setBusy] = useState(false);

  const options = useMemo<QuestionOption[]>(
    () => [
      { slug: "friends_colleagues", icon: <Users className="h-4 w-4" />, label: t(($) => $.questions.source.friends_colleagues) },
      { slug: "search", icon: <GoogleIcon className="h-[18px] w-[18px]" />, label: t(($) => $.questions.source.search) },
      { slug: "social_x", icon: <XIcon className="h-[15px] w-[15px]" />, label: t(($) => $.questions.source.social_x) },
      { slug: "social_linkedin", icon: <LinkedInIcon className="h-[18px] w-[18px]" />, label: t(($) => $.questions.source.social_linkedin) },
      { slug: "social_youtube", icon: <YouTubeIcon className="h-[18px] w-[18px]" />, label: t(($) => $.questions.source.social_youtube) },
      { slug: "social_github", icon: <GitHubIcon className="h-[18px] w-[18px]" />, label: t(($) => $.questions.source.social_github) },
      { slug: "social_other", icon: <Globe className="h-4 w-4" />, label: t(($) => $.questions.source.social_misc) },
      { slug: "blog_newsletter", icon: <Newspaper className="h-4 w-4" />, label: t(($) => $.questions.source.blog_newsletter) },
      { slug: "ai_assistant", icon: <OpenAIIcon className="h-[16px] w-[16px]" />, label: t(($) => $.questions.source.ai_assistant) },
      { slug: "from_work", icon: <Briefcase className="h-4 w-4" />, label: t(($) => $.questions.source.from_work) },
      { slug: "event_conference", icon: <CalendarDays className="h-4 w-4" />, label: t(($) => $.questions.source.event_conference) },
      { slug: "dont_remember", icon: <HelpCircle className="h-4 w-4" />, label: t(($) => $.questions.source.dont_remember) },
      { slug: "other", icon: <MoreHorizontal className="h-4 w-4" />, label: t(($) => $.questions.source.other), isOther: true },
    ],
    [t],
  );

  // 单选：`source` 数组里同时最多一个 slug。服务端 schema 保留数组形态
  // （向后兼容 v2 多选结构以及已存在的行），但弹窗 UI 只提交一个选项——
  // 归因问题按行业惯例（Fairing、Recast、HockeyStack 都是单选）只问「最主要
  // 来源」，这样团队拿到的是干净的渠道权重，而不是把用户拆进 N 个桶里。
  const pickedSlug: string | null = answers.source[0] ?? null;
  const otherOption = options.find((o) => o.isOther) ?? null;
  const otherSelected = pickedSlug === otherOption?.slug;
  const otherFilled = (answers.source_other ?? "").trim().length > 0;
  const canSubmit =
    !busy &&
    pickedSlug !== null &&
    // 选了「其他」的话，提交前必须有实际输入——空白的 Other 不算有效归因数据。
    (!otherSelected || otherFilled);

  const handleSelect = useCallback(
    (option: QuestionOption) => {
      if (option.isOther) {
        const slug = option.slug as Source;
        setAnswers((a) => ({
          ...a,
          source: [slug],
          // 从「其他」再选回「其他」时，不把上一次的输入文字带过来：输入框会
          // 重新聚焦、保持空白让用户直接打。残留的旧文本只会误导。
          source_other: a.source[0] === "other" ? a.source_other : null,
          source_skipped: false,
        }));
        return;
      }
      const slug = option.slug as Source;
      setAnswers((a) => ({
        ...a,
        source: [slug],
        source_other: null,
        source_skipped: false,
      }));
    },
    [],
  );

  const handleOtherChange = useCallback((value: string) => {
    setAnswers((a) => ({ ...a, source_other: value }));
  }, []);

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    try {
      // PATCH /api/me/onboarding 是整块覆盖 JSONB 的，所以这里从 auth store
      // 重新读出已存答案、只覆盖 source 槽位——保住本弹窗目标人群（老用户）的
      // role / use_case / version 不被冲掉。
      const stored =
        useAuthStore.getState().user?.onboarding_questionnaire ?? null;
      await saveQuestionnaire(
        mergedQuestionnairePatch(stored, {
          source: answers.source,
          source_other: answers.source_other,
          source_skipped: false,
        }),
      );
      onComplete();
    } catch (err) {
      setBusy(false);
      toast.error(
        err instanceof Error
          ? err.message
          : t(($) => $.source_backfill.save_failed_toast),
      );
    }
  }, [canSubmit, answers.source, answers.source_other, onComplete, t]);

  const skip = useCallback(async () => {
    if (busy) return;
    setBusy(true);
    try {
      const stored =
        useAuthStore.getState().user?.onboarding_questionnaire ?? null;
      await saveQuestionnaire(
        mergedQuestionnairePatch(stored, {
          source: [],
          source_other: null,
          source_skipped: true,
        }),
      );
      onComplete();
    } catch (err) {
      setBusy(false);
      toast.error(
        err instanceof Error
          ? err.message
          : t(($) => $.source_backfill.save_failed_toast),
      );
    }
  }, [busy, onComplete, t]);

  return (
    <DialogContent className="sm:max-w-2xl p-0 gap-0 overflow-hidden">
      <div className="px-6 pt-6 pb-2">
        <div className="text-micro font-medium uppercase tracking-[0.08em] text-muted-foreground">
          {t(($) => $.source_backfill.eyebrow)}
        </div>
        <h2 className="mt-1 text-balance font-serif text-display-sm font-medium leading-tight tracking-tight text-foreground">
          {t(($) => $.questions.source.question)}
        </h2>
        <p className="mt-2 text-body text-muted-foreground">
          {t(($) => $.source_backfill.lede)}
        </p>
      </div>

      <fieldset
        role="radiogroup"
        aria-label={t(($) => $.questions.source.question)}
        className="m-0 grid grid-cols-1 gap-2 p-0 px-6 pt-4 sm:grid-cols-2"
      >
        {options.map((option) =>
          option.isOther ? (
            <IconOtherOptionCard
              key={option.slug}
              icon={option.icon}
              label={option.label}
              selected={otherSelected}
              onSelect={() => handleSelect(option)}
              otherValue={answers.source_other ?? ""}
              onOtherChange={handleOtherChange}
              onConfirm={submit}
              placeholder={t(($) => $.questions.source.other_placeholder)}
              mode="radio"
            />
          ) : (
            <IconOptionCard
              key={option.slug}
              icon={option.icon}
              label={option.label}
              selected={pickedSlug === option.slug}
              onSelect={() => handleSelect(option)}
              mode="radio"
            />
          ),
        )}
      </fieldset>

      <div className="mt-4 flex flex-wrap items-center justify-end gap-x-4 gap-y-2 border-t bg-muted/40 px-6 py-3">
        <span
          aria-live="polite"
          className="mr-auto text-caption text-muted-foreground"
        >
          {canSubmit
            ? t(($) => $.source_backfill.hint_ready)
            : t(($) => $.step_question.hint_pick)}
        </span>
        <div className="flex items-center gap-2">
          <Button variant="secondary" disabled={busy} onClick={skip}>
            {t(($) => $.common.skip)}
          </Button>
          <Button disabled={!canSubmit} onClick={submit}>
            {t(($) => $.source_backfill.submit)}
          </Button>
        </div>
      </div>
    </DialogContent>
  );
}
