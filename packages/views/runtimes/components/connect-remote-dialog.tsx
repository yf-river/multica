// ============================================================================
// 连接远程机器对话框（ConnectRemoteDialog）——「如何把一台远程电脑接入工作区」
//
// 角色定位：runtimes 列表页「连接远程」入口打开的引导弹窗。它不替用户执行任何
// 安装/配置动作，只负责三件事：
//  1. 展示逐条命令行（装 daemon → 指向服务器 → 启动），带一键复制按钮；
//  2. 被动等待结果：`multica setup` 成功后 daemon 会广播 `daemon:register` WebSocket
//     事件，对话框监听到后自动从「指令页」切到「成功页」（零轮询）；
//  3. 成功后提供「去创建 agent」与「查看新 runtime」两个跳转出口。
//
// 细节约定：
//  - 命令面向两类部署——自托管（配置里给了 server/app URL 时，命令指向自建服务）
//    与 SaaS 云（没有自托管配置时回退到 https://api.multica.ai / https://multica.ai）。
//  - 复制按钮的「已复制」状态 2 秒后自动复位。
// ============================================================================
"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Check, ChevronRight, Copy, Terminal } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeKeys } from "@multica/core/runtimes/queries";
import { useWSEvent } from "@multica/core/realtime";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import { useConfigStore } from "@multica/core/config";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { CODE_LIGATURE_CLASS } from "@multica/ui/lib/code-style";
import { copyText } from "@multica/ui/lib/clipboard";
import {
  UI_EASE_OUT,
  UI_MOTION_DURATION,
} from "@multica/ui/lib/motion";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

// 弹窗的两个阶段：先看「指令页」，收到 daemon 注册事件后切到「成功页」。
type Step = "instructions" | "success";

// 官方安装脚本：一条 curl|bash 把 multica CLI/daemon 装进远程机器。
const INSTALL_CMD =
  "curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash";
// 兜底用的云端地址。只有当部署给用户配置了自托管 server/app URL 时，
// 生成的命令才会指向自建服务；否则一律指回 multica 官方云（见 daemonCommands）。
const CLOUD_SERVER_URL = "https://api.multica.ai";
const CLOUD_APP_URL = "https://multica.ai";

// 把配置里的 URL 规整成可拼进命令的样子：去首尾空白、去掉末尾斜杠，
// 避免拼出 "--server-url https://xxx/" 这种尾斜杠不一致的命令。
function normalizeCommandURL(url: string | undefined) {
  return url?.trim().replace(/\/+$/, "") ?? "";
}

// 生成让用户在远程机器上执行的 daemon 命令。
//
// 两种情况：
//  - 自托管：server_url 与 app_url 都被配置过 → 用 multica setup self-host
//    一条命令完成「写配置 + 登录 + 启动 daemon」；
//  - 没配置（SaaS 用户）→ setup 命令走交互式默认，token 命令则显式指向
//    CLOUD_SERVER_URL / CLOUD_APP_URL，保证连的是官方云。
function daemonCommands(serverUrl: string | undefined, appUrl: string | undefined) {
  const normalizedServerUrl = normalizeCommandURL(serverUrl);
  const normalizedAppUrl = normalizeCommandURL(appUrl);
  if (normalizedServerUrl && normalizedAppUrl) {
    return {
      setupCmd: `multica setup self-host --server-url ${normalizedServerUrl} --app-url ${normalizedAppUrl}`,
      tokenCmd: `multica config set server_url ${normalizedServerUrl}
multica config set app_url ${normalizedAppUrl}
multica login --token <YOUR_TOKEN>
multica daemon start`,
    };
  }

  return {
    setupCmd: "multica setup",
    tokenCmd: `multica config set server_url ${CLOUD_SERVER_URL}
multica config set app_url ${CLOUD_APP_URL}
multica login --token <YOUR_TOKEN>
multica daemon start`,
  };
}

/**
 * 连接远程机器对话框：展示分步命令行，指导用户把新机器接入当前工作区。
 *
 * @param onClose - 请求关闭对话框。
 *
 * 内部状态机只有两步：instructions（指令页）→ success（成功页）。本组件不做
 * 轮询——用户执行 setup 后 daemon 广播的 `daemon:register` WS 事件是唯一的
 * 「成功了」信号，收到才切页（详见 handleDaemonRegister）。
 */
export function ConnectRemoteDialog({ onClose }: { onClose: () => void }) {
  const [step, setStep] = useState<Step>("instructions");
  const wsId = useWorkspaceId();
  const slug = useWorkspaceSlug();
  const qc = useQueryClient();
  const navigation = useNavigation();
  const shouldReduceMotion = useReducedMotion() ?? false;
  // 记住「新注册成功的 runtime id」，成功页的「查看 runtime」跳转要用它；
  // 放在 ref 里是因为它只服务于跳转回调，不值得为它触发一次重渲染。
  const newRuntimeIdRef = useRef<string | null>(null);

  // `multica setup` 是一条把「写配置 + 登录 + 启动 daemon」串在一起的阻塞式命令，
  // 本对话框被动监听它启动成功后广播的 `daemon:register` WS 事件，收到即认为
  // 连接建立、自动切到成功页——不需要轮询状态，也不会误判未完成的 setup。
  const handleDaemonRegister = useCallback(
    (payload: unknown) => {
      if (step !== "instructions") return;
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
      const p = payload as Record<string, unknown> | null;
      if (p?.runtime_id && typeof p.runtime_id === "string") {
        newRuntimeIdRef.current = p.runtime_id;
      }
      setStep("success");
    },
    [step, qc, wsId],
  );
  useWSEvent("daemon:register", handleDaemonRegister);

  const handleGoToAgents = () => {
    onClose();
    if (slug) {
      navigation.push(paths.workspace(slug).agents());
    }
  };

  const handleGoToRuntime = () => {
    onClose();
    if (slug && newRuntimeIdRef.current) {
      navigation.push(
        paths.workspace(slug).runtimeDetail(newRuntimeIdRef.current),
      );
    }
  };

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="flex max-h-[85vh] flex-col gap-0 p-0 sm:max-w-lg">
        <AnimatePresence mode="wait" initial={false}>
          <motion.div
            key={step}
            className="flex min-h-0 flex-1 flex-col"
            initial={{
              opacity: 0,
              transform: shouldReduceMotion
                ? "translateY(0)"
                : "translateY(8px)",
            }}
            animate={{
              opacity: 1,
              transform: "translateY(0)",
              transition: {
                duration: shouldReduceMotion
                  ? UI_MOTION_DURATION.fast
                  : UI_MOTION_DURATION.standard,
                ease: UI_EASE_OUT,
              },
            }}
            exit={{
              opacity: 0,
              transform: shouldReduceMotion
                ? "translateY(0)"
                : "translateY(-8px)",
              transition: {
                duration: shouldReduceMotion
                  ? UI_MOTION_DURATION.fast
                  : UI_MOTION_DURATION.micro,
                ease: UI_EASE_OUT,
              },
            }}
          >
            {step === "instructions" ? (
              <InstructionsStep onClose={onClose} />
            ) : (
              <SuccessStep
                onGoToAgents={handleGoToAgents}
                onGoToRuntime={
                  newRuntimeIdRef.current ? handleGoToRuntime : undefined
                }
              />
            )}
          </motion.div>
        </AnimatePresence>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// 复制按钮 + 命令行展示块。与 onboarding 的 CliInstallInstructions 共用同一套
// 视觉与交互（改动时注意两边保持一致）。
// ---------------------------------------------------------------------------

function CopyButton({ text, ariaLabel }: { text: string; ariaLabel: string }) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 2000);
    return () => clearTimeout(t);
  }, [copied]);

  const handleCopy = () => {
    void copyText(text).then((ok) => {
      if (ok) setCopied(true);
    });
  };

  return (
    <button
      type="button"
      onClick={handleCopy}
      aria-label={ariaLabel}
      className="shrink-0 rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-success" aria-hidden />
      ) : (
        <Copy className="h-3.5 w-3.5" aria-hidden />
      )}
    </button>
  );
}

function CommandStep({
  n,
  label,
  cmd,
  copyAria,
}: {
  n: number;
  label: string;
  cmd: string;
  copyAria: string;
}) {
  return (
    <div>
      <p className="mb-1.5 text-caption font-medium text-foreground">
        {n}. {label}
      </p>
      <div className="flex items-start gap-2 rounded-lg bg-muted px-3 py-2.5 font-mono text-body">
        <Terminal
          className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground"
          aria-hidden
        />
        <code
          className={cn(
            "min-w-0 flex-1 break-all whitespace-pre-wrap tabular-nums",
            CODE_LIGATURE_CLASS,
          )}
        >
          {cmd}
        </code>
        <CopyButton text={cmd} ariaLabel={copyAria} />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 第 1 步：指令页——安装脚本、连接命令、实时监听提示与排障折叠区。
// ---------------------------------------------------------------------------

function InstructionsStep({ onClose }: { onClose: () => void }) {
  const { t } = useT("runtimes");
  const daemonServerUrl = useConfigStore((s) => s.daemonServerUrl);
  const daemonAppUrl = useConfigStore((s) => s.daemonAppUrl);
  const { setupCmd, tokenCmd } = daemonCommands(daemonServerUrl, daemonAppUrl);
  return (
    <>
      <DialogHeader className="px-6 pt-6 pb-2">
        <DialogTitle className="text-title-sm text-balance">
          {t(($) => $.connect.title)}
        </DialogTitle>
        <DialogDescription className="text-caption text-balance">
          {t(($) => $.connect.description)}
        </DialogDescription>
      </DialogHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-6 py-4">
        <div className="space-y-4">
          <CommandStep
            n={1}
            label={t(($) => $.connect.step1_label)}
            cmd={INSTALL_CMD}
            copyAria={t(($) => $.connect.copy_aria)}
          />

          <div>
            <CommandStep
              n={2}
              label={t(($) => $.connect.step2_label)}
              cmd={setupCmd}
              copyAria={t(($) => $.connect.copy_aria)}
            />
            <p className="mt-1.5 text-micro leading-[1.55] text-muted-foreground">
              {t(($) => $.connect.step2_hint)}
            </p>
          </div>

          <LiveListening />

          <TroubleshootingDetails tokenCmd={tokenCmd} />
        </div>
      </div>

      <DialogFooter className="m-0 rounded-b-xl border-t bg-muted/30 px-6 py-3">
        <Button variant="outline" size="sm" onClick={onClose}>
          {t(($) => $.connect.cancel)}
        </Button>
      </DialogFooter>
    </>
  );
}

function TroubleshootingDetails({ tokenCmd }: { tokenCmd: string }) {
  const { t } = useT("runtimes");
  return (
    <details className="group rounded-lg border border-dashed">
      <summary className="flex cursor-pointer list-none items-center gap-1.5 px-3 py-2 text-caption font-medium text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <ChevronRight
          className="h-3 w-3 transition-transform group-open:rotate-90"
          aria-hidden
        />
        {t(($) => $.connect.troubleshooting)}
      </summary>
      <div className="space-y-2 border-t px-3 pt-2.5 pb-3 text-micro leading-[1.55] text-muted-foreground">
        <p>{t(($) => $.connect.trouble_intro)}</p>
        <CommandStep
          n={2}
          label={t(($) => $.connect.step2_label)}
          cmd={tokenCmd}
          copyAria={t(($) => $.connect.copy_aria)}
        />
        <p>
          {t(($) => $.connect.trouble_token_hint_prefix)}
          <span className="font-medium text-foreground">
            {t(($) => $.connect.trouble_token_hint_destination)}
          </span>
          {t(($) => $.connect.trouble_token_hint_suffix)}
        </p>
        <ul className="space-y-1">
          <li className="flex items-center gap-1.5">
            <span>{t(($) => $.connect.trouble_check_status)}</span>
            {/* CLI 命令是字面 shell 字符串，不属于 i18n 文案，直接写死。 */}
            <code
              className={cn(
                "rounded bg-muted px-1.5 py-0.5 font-mono text-micro text-foreground",
                CODE_LIGATURE_CLASS,
              )}
            >
              {"multica daemon status"}
            </code>
          </li>
          <li className="flex items-center gap-1.5">
            <span>{t(($) => $.connect.trouble_view_logs)}</span>
            {/* CLI 命令是字面 shell 字符串，不属于 i18n 文案，直接写死。 */}
            <code
              className={cn(
                "rounded bg-muted px-1.5 py-0.5 font-mono text-micro text-foreground",
                CODE_LIGATURE_CLASS,
              )}
            >
              {"multica daemon logs -f"}
            </code>
          </li>
        </ul>
      </div>
    </details>
  );
}

// ---------------------------------------------------------------------------
// 「实时监听中」提示条：带呼吸圆点，向用户传达「对话框正等着你执行完命令、
// daemon 一注册就会自动前进」。
// ---------------------------------------------------------------------------

function LiveListening() {
  const { t } = useT("runtimes");
  return (
    <div
      className="flex items-center gap-2.5 rounded-lg border bg-muted/40 px-3 py-2.5 text-caption"
      role="status"
      aria-live="polite"
    >
      <span className="relative inline-flex shrink-0" aria-hidden>
        <span className="absolute inline-flex h-2 w-2 animate-ping rounded-full bg-success opacity-60 motion-reduce:hidden" />
        <span className="relative inline-flex h-2 w-2 rounded-full bg-success" />
      </span>
      <span className="font-medium text-foreground">
        {t(($) => $.connect.live_listening)}
      </span>
      <span className="text-muted-foreground">
        {t(($) => $.connect.live_listening_hint)}
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 第 2 步：成功页——对勾图标 + 「去创建 agent / 查看新 runtime」两个出口。
// ---------------------------------------------------------------------------

function SuccessStep({
  onGoToAgents,
  onGoToRuntime,
}: {
  onGoToAgents: () => void;
  onGoToRuntime?: () => void;
}) {
  const { t } = useT("runtimes");
  return (
    <>
      <DialogHeader className="px-6 pt-6 pb-2">
        <DialogTitle className="text-title-sm text-balance">
          {t(($) => $.connect.success_title)}
        </DialogTitle>
        <DialogDescription className="text-caption text-balance">
          {t(($) => $.connect.success_description)}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col items-center gap-3 px-6 py-8">
        <div
          className="flex h-12 w-12 items-center justify-center rounded-full bg-success/10"
          aria-hidden
        >
          <Check className="h-6 w-6 text-success" />
        </div>
      </div>

      <DialogFooter className="m-0 rounded-b-xl border-t bg-muted/30 px-6 py-3">
        {onGoToRuntime && (
          <Button variant="ghost" size="sm" onClick={onGoToRuntime}>
            {t(($) => $.connect.view_runtime)}
          </Button>
        )}
        <Button size="sm" onClick={onGoToAgents}>
          {t(($) => $.connect.create_agent)}
          <ChevronRight className="h-3.5 w-3.5" aria-hidden />
        </Button>
      </DialogFooter>
    </>
  );
}
