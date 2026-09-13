"use client";

// 引导第 3 步「连上你的电脑/云端」：从浏览器端接上一个运行时（daemon）。主路径
// 是弹窗里的 CLI 安装 + 实时探测；另有两张备选卡片（桌面版下载、云端“即将到来”）。
import { useEffect, useState, type ReactNode } from "react";
import { ArrowRight, Terminal, Loader2, MonitorDown } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import type { AgentRuntime } from "@multica/core/types";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import {
  StepFooter,
  StepHeading,
} from "../components/step-shell";
import {
  MikaRuntimeChoice,
  type MikaRuntimeSelection,
} from "../../runtimes/components/mika-runtime-choice";
import { useRuntimePicker } from "../components/use-runtime-picker";
import { useT } from "../../i18n";

// 当前打开的对话框类型（目前只有 CLI 安装弹窗）。
type DialogState = "cli" | null;

/**
 * 第 3 步在浏览器端连接一个运行时（daemon）。本屏是一个分叉选择：
 *
 *   1. **安装 CLI** —— 主卡片：黑底，「查看步骤」胶囊 → 打开一个包含真实安装
 *      指引 + 实时运行时探测的弹窗。探测到运行时且用户选中后，弹窗里的
 *      「连接并继续」按钮会触发 `onNext(runtime)` 并推进流程。
 *   2. **云端电脑** —— 备选卡片，带「即将推出」徽标。目前不可用，只渲染成
 *      一个静态的、不可点的预览。
 *
 * 页脚做了简化——没有 Continue 按钮，因为推进由 CLI 弹窗自己负责，只剩 Skip。
 *
 * @param wsId 目标工作区 id
 * @param wsSlug 目标工作区 slug
 * @param onNext 选定运行时后的回调（null 表示跳过，不带运行时）
 * @param cliInstructions 平台相关的 CLI 安装说明卡（渲染在 CLI 弹窗内）
 */
export function StepPlatformFork({
  wsId,
  wsSlug,
  onNext,
  cliInstructions,
}: {
  wsId: string;
  /** 目标工作区 slug。显式传参，让运行时列表读「正在配置的工作区」，
   *  而不是应用当前显示的那个工作区。 */
  wsSlug?: string;
  onNext: (runtime: AgentRuntime | null, model?: string) => void | Promise<void>;
  /** 平台相关的 CLI 安装说明卡，渲染在 CLI 弹窗里。 */
  cliInstructions?: ReactNode;
}) {
  const { t } = useT("onboarding");

  const [dialog, setDialog] = useState<DialogState>(null);
  const [connecting, setConnecting] = useState(false);
  const [model, setModel] = useState("");

  const picker = useRuntimePicker(wsId, wsSlug);

  const handleOpenCli = () => {
    setDialog("cli");
  };

  const handleCliConnect = async () => {
    if (!picker.selected || connecting) return;
    setConnecting(true);
    try {
      await onNext(picker.selected, model || undefined);
      setDialog(null);
    } finally {
      setConnecting(false);
    }
  };

  const footerHint = t(($) => $.step_platform.hint_default);

  return (
    <>
      <div className="flex flex-col gap-8 pt-2 sm:pt-6">
        {/* 眉题本来要写成「Connect a computer」，而它正下方的标题开头就是这三个
            同样的词。这个区块没有眉题插槽、进度栏也已经说了这是哪一步，于是删掉。 */}
        <StepHeading
          title={t(($) => $.step_platform.headline)}
          description={t(($) => $.step_platform.lede)}
        />

        <div className="flex flex-col gap-2">
          <ForkPrimary onClick={handleOpenCli} />

          <ForkAlt
            title={t(($) => $.step_platform.desktop_title)}
            subtitle={t(($) => $.step_platform.desktop_subtitle)}
            actionLabel={t(($) => $.step_platform.desktop_action)}
            icon={<MonitorDown className="h-4 w-4" aria-hidden />}
            onAction={() =>
              window.open("/download", "_blank", "noopener,noreferrer")
            }
          />

          <ForkAlt
            title={t(($) => $.step_platform.cloud_title)}
            subtitle={t(($) => $.step_platform.cloud_subtitle)}
            actionLabel={t(($) => $.step_platform.cloud_action)}
            disabled
          />
        </div>

      </div>

      {/* CLI 路径的推进由 CLI 弹窗自己的「连接并继续」按钮负责；Skip 则创建那条
          自服务的引导任务。 */}
      <StepFooter hint={footerHint}>
        <Button
          variant="ghost"
          className="w-full"
          onClick={() => onNext(null)}
        >
          {t(($) => $.step_runtime.skip)}
        </Button>
      </StepFooter>

    <CliInstallDialog
      open={dialog === "cli"}
      onClose={() => setDialog(null)}
      onConnect={handleCliConnect}
      runtimes={picker.runtimes}
      choice={{ runtimeId: picker.selectedId ?? "", model }}
      onChoiceChange={(next) => {
        if (next.runtimeId !== picker.selectedId) {
          picker.setSelectedId(next.runtimeId);
        }
        setModel(next.model);
      }}
      hasRuntimes={picker.hasRuntimes}
      canConnect={picker.selected !== null}
      selectedName={
        picker.selected ? runtimeDisplayLabel(picker.selected) : null
      }
      connecting={connecting}
      cliInstructions={cliInstructions}
    />
    </>
  );
}

// ------------------------------------------------------------
// 分叉卡片
// ------------------------------------------------------------

/** 主卡片（安装 CLI）：整块黑色可点击横幅，点击打开 CLI 安装弹窗。 */
function ForkPrimary({ onClick }: { onClick: () => void }) {
  const { t } = useT("onboarding");
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "group flex items-center justify-between gap-4 rounded-xl bg-foreground px-6 py-5 text-left text-background transition-transform",
        "hover:-translate-y-0.5",
      )}
    >
      <div className="min-w-0">
        <div className="flex items-center gap-2 text-title font-medium tracking-tight">
          <Terminal className="h-4 w-4" aria-hidden />
          {t(($) => $.step_platform.cli_title)}
        </div>
        <div className="mt-1 text-label text-background/60">
          {t(($) => $.step_platform.cli_subtitle)}
        </div>
      </div>
      <span
        aria-hidden
        className="inline-flex shrink-0 items-center gap-1.5 rounded-full bg-background/10 px-4 py-2 text-label font-medium transition-colors group-hover:bg-background/20"
      >
        {t(($) => $.step_platform.cli_action)}
        <ArrowRight className="h-3.5 w-3.5" />
      </span>
    </button>
  );
}

/**
 * 备选卡片：右侧带一个操作按钮。`disabled` 时操作渲染成静态徽标（用于还没接线的
 * 「即将推出」路径）；否则是 outline 按钮，点击触发 `onAction`，通常用来打开
 * 弹窗或跳转下载页。
 */
function ForkAlt({
  title,
  subtitle,
  actionLabel,
  onAction,
  disabled = false,
  icon,
}: {
  title: string;
  subtitle: ReactNode;
  actionLabel: ReactNode;
  onAction?: () => void;
  disabled?: boolean;
  icon?: ReactNode;
}) {
  return (
    <div
      className={cn(
        "flex items-center justify-between gap-4 rounded-lg border bg-card px-5 py-4",
        disabled && "opacity-70",
      )}
    >
      <div className="min-w-0">
        <div className="flex items-center gap-2 text-body font-medium text-foreground">
          {icon}
          {title}
        </div>
        <div className="mt-1 text-caption leading-[1.5] text-muted-foreground">
          {subtitle}
        </div>
      </div>
      {disabled ? (
        <span className="shrink-0 rounded-full border bg-muted px-3 py-1 text-caption font-medium text-muted-foreground">
          {actionLabel}
        </span>
      ) : (
        <Button
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={onAction}
        >
          {actionLabel}
        </Button>
      )}
    </div>
  );
}

// ------------------------------------------------------------
// CLI 安装弹窗
// ------------------------------------------------------------

/**
 * CLI 安装路径的模态弹窗：装真实安装指引卡（通过 `cliInstructions` 插槽）加
 * 实时运行时探测。自己负责「连接并继续」的推进——当有运行时注册、且用户选中它
 * 之后，点这个按钮会关掉弹窗并触发父组件的 `onConnect`。
 */
function CliInstallDialog({
  open,
  onClose,
  onConnect,
  runtimes,
  choice,
  onChoiceChange,
  hasRuntimes,
  canConnect,
  selectedName,
  connecting,
  cliInstructions,
}: {
  open: boolean;
  onClose: () => void;
  onConnect: () => void | Promise<void>;
  runtimes: AgentRuntime[];
  choice: MikaRuntimeSelection;
  onChoiceChange: (next: MikaRuntimeSelection) => void;
  hasRuntimes: boolean;
  canConnect: boolean;
  selectedName: string | null;
  connecting: boolean;
  cliInstructions?: ReactNode;
}) {
  const { t } = useT("onboarding");
  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.step_platform.cli_dialog_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.step_platform.cli_dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto pt-2">
          {cliInstructions}

          {hasRuntimes ? (
            <>
              <div className="flex items-center gap-2 pt-1 text-body">
                <div className="h-2 w-2 rounded-full bg-success" />
                <span className="font-medium">
                  {t(($) => $.step_platform.runtimes_connected, { count: runtimes.length })}
                </span>
              </div>
              {/* 运行时列表最多显示约 4 行、其余滚动。这样即使注册了很多台机器，
                  上面的命令也始终够得着。 */}
              <MikaRuntimeChoice
                layout="list"
                runtimes={runtimes}
                value={choice}
                onChange={onChoiceChange}
                disabled={connecting}
              />
            </>
          ) : (
            <CliWaitingStatus dialogOpen={open} />
          )}
        </div>

        <DialogFooter className="flex items-center justify-between gap-3 sm:justify-between">
          {/* 提示只在运行时注册之后才有意义——「选一个」/「已选 X」。还在等待时，
              正文里的 CliWaitingStatus 已经表达了实时监听的姿态，页脚再写一句
              「等待中…」就是重复。 */}
          <span className="text-caption text-muted-foreground">
            {hasRuntimes
              ? canConnect && selectedName
                ? t(($) => $.step_runtime.hint_selected, { name: selectedName })
                : t(($) => $.step_platform.cli_dialog_pick_hint)
              : null}
          </span>
          <div className="flex items-center gap-2">
            <Button variant="ghost" onClick={onClose}>
              {t(($) => $.common.cancel)}
            </Button>
            <Button disabled={!canConnect || connecting} onClick={onConnect}>
              {connecting && <Loader2 className="h-4 w-4 animate-spin" />}
              {t(($) => $.step_runtime.continue)}
              <ArrowRight className="h-4 w-4" />
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * 把秒数格式化成 `m:ss`（如 75 → "1:15"）。行内小工具——现有工具里没有同款
 * 格式（agent-live-card 的 formatElapsed 是「1m 15s」风格，不适合滴答跳动的时钟）。
 */
function formatElapsed(seconds: number) {
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}

/**
 * CLI 弹窗的等待态——在第一个 daemon 注册之前一直显示。前端其实观测不到
 * 安装 / 登录 / daemon 启动这些阶段（它们发生在用户的终端和浏览器里），
 * 所以只能做到：
 *
 *   1. 确认「我们在听」——一个跳动的绿点 + m:ss 计时器表示 WS 订阅活着
 *      （useRuntimePicker 已经订阅了 `daemon:register`）。这就是在告诉用户
 *      「系统没卡死，它在等你的 daemon」。
 *   2. 随着等待时间越过不同阈值，逐步放出排障提示——让卡在安装中途的用户
 *      得到有用指引，而不是在 t=0 就被灌一堆话。
 *   3. 到 90s+ 的「停滞」档，引导用户去看备选路径。
 *
 * 计时器只在弹窗打开时走动，所以关掉再开后计时会从零开始。
 */
function CliWaitingStatus({ dialogOpen }: { dialogOpen: boolean }) {
  const { t } = useT("onboarding");
  const [elapsed, setElapsed] = useState(0);

  useEffect(() => {
    if (!dialogOpen) {
      setElapsed(0);
      return;
    }
    const id = window.setInterval(() => {
      setElapsed((e) => e + 1);
    }, 1000);
    return () => window.clearInterval(id);
  }, [dialogOpen]);

  // 阶段阈值是估算的——`multica setup` 的典型流程是
  //   ~1s 保存配置 → 浏览器标签页授权（人操作，5–30s）→
  //   ~2s daemon 启动 → 立即 WS 注册。所以 15s 内算「正常」，15–45s 大概率
  //   卡在浏览器授权，45–90s 多半是终端里报错了，90s+ 就是「什么都进不来，
  //   去推荐备选路径」。
  const stage: "normal" | "midway" | "slow" | "stalled" =
    elapsed < 15
      ? "normal"
      : elapsed < 45
        ? "midway"
        : elapsed < 90
          ? "slow"
          : "stalled";

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-muted/30 p-4">
      <div className="flex items-center gap-2 text-body">
        {/* 跳动的绿点表示 WS 订阅存活——useRuntimePicker 已订阅 `daemon:register`，
            这是「我们在听」的视觉确认。 */}
        <span
          aria-hidden
          className="inline-block size-2 shrink-0 rounded-full bg-success animate-pulse"
        />
        <span className="font-medium text-foreground">
          {t(($) => $.step_platform.live_listening)}
        </span>
        <span className="ml-auto font-mono text-caption tabular-nums text-muted-foreground">
          {formatElapsed(elapsed)}
        </span>
      </div>

      <p
        aria-live="polite"
        className="text-caption leading-[1.55] text-muted-foreground"
      >
        {stage === "normal" && (
          <>
            {t(($) => $.step_platform.stage_normal_prefix)}
            <span className="font-mono">{"multica setup"}</span>
            {t(($) => $.step_platform.stage_normal_suffix)}
          </>
        )}
        {stage === "midway" && (
          <>
            {t(($) => $.step_platform.stage_midway_prefix)}
            <span className="font-mono">{"multica setup"}</span>
            {t(($) => $.step_platform.stage_midway_suffix)}
          </>
        )}
        {stage === "slow" && (
          <>
            {t(($) => $.step_platform.stage_slow_prefix)}
            <span className="font-mono">{"multica setup"}</span>
            {t(($) => $.step_platform.stage_slow_suffix)}
          </>
        )}
        {stage === "stalled" && (
          <>
            {t(($) => $.step_platform.stage_stalled_prefix)}
            <span className="font-medium text-foreground">{t(($) => $.step_platform.stage_stalled_term)}</span>
            {t(($) => $.step_platform.stage_stalled_suffix)}
          </>
        )}
      </p>
    </div>
  );
}
