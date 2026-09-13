"use client";

// 首次使用引导的主流程：编排 Welcome → 问卷（关于你）→ 工作区 → 运行时选择，
// 负责各步之间的前进 / 返回 / 跳过，并把问卷答案即时持久化到服务端。
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { useAuthStore } from "@multica/core/auth";
import {
  completeOnboarding,
  ONBOARDING_STEP_ORDER,
  saveQuestionnaire,
  useBootstrapMika,
  useWelcomeStore,
  type OnboardingStep,
  type QuestionnaireAnswers,
} from "@multica/core/onboarding";
import { useWorkspaceList } from "@multica/core/workspace";
import type { AgentRuntime, Workspace } from "@multica/core/types";
import { StepWelcome } from "./steps/step-welcome";
import { StepShell } from "./components/step-shell";
import { StepAboutYou } from "./steps/step-about-you";
import { StepWorkspace } from "./steps/step-workspace";
import { StepPlatformFork } from "./steps/step-platform-fork";
import { OnboardingLogoutButton } from "./components/onboarding-logout-button";
import { getMikaOnboarding } from "./templates";
import { useT } from "../i18n";

// 问卷空默认值：所有选项清空、跳过标记置否、version 固定为 2。
const EMPTY_QUESTIONNAIRE: QuestionnaireAnswers = {
  source: [],
  source_other: null,
  source_skipped: false,
  role: null,
  role_other: null,
  role_skipped: false,
  use_case: [],
  use_case_other: null,
  use_case_skipped: false,
  version: 2,
};

/**
 * 把存下来的问卷槽位收敛成当前 UI 需要的数组形状。早期版本把 `source` /
 * `use_case` 存成单个字符串，这里在读入时做兼容，保证在本次改动之前开始引导
 * 的用户，重新进来时不会看到自己之前的答案凭空消失。空串和 null 都归为 []。
 */
function coerceToArray<T extends string>(value: unknown): T[] {
  if (Array.isArray(value)) {
    return value.filter((v): v is T => typeof v === "string" && v.length > 0);
  }
  if (typeof value === "string" && value.length > 0) {
    return [value as T];
  }
  return [];
}

/**
 * 把服务端已存的答案并进空默认值。再次进入引导时，已答过的槽位会预填，但
 * `*_skipped` 一律视为「没跳过过」（这次用户可以作答）——v1 的跳过标记在读入时
 * 丢弃，之前那次跳过的统计记录仍留在数据库里。
 */
function mergeQuestionnaire(
  raw: Record<string, unknown>,
): QuestionnaireAnswers {
  const merged = {
    ...EMPTY_QUESTIONNAIRE,
    ...(raw as Partial<QuestionnaireAnswers>),
  };
  return {
    ...merged,
    source: coerceToArray<QuestionnaireAnswers["source"][number]>(raw.source),
    use_case: coerceToArray<QuestionnaireAnswers["use_case"][number]>(
      raw.use_case,
    ),
    source_skipped: false,
    role_skipped: false,
    use_case_skipped: false,
  };
}

/**
 * 完成引导后的去向契约（StepShell 的 onComplete）：带着工作区 + 一个可选的目的地。
 * 连上运行时的引导会打开最终步创建出的真实 Mika 对话；其余出口落在工作区的
 * 任务列表页。
 *
 * 三种退出形态都会喂给 onComplete：
 *   - 欢迎页选「我以前做过」（跳过已有工作区）：completeOnboarding 把用户标记为
 *     已引导，然后跳到已有工作区的任务列表。
 *   - 跳过运行时（第 3 步没有运行时）：completeOnboarding 标记已引导；这里再往
 *     welcome store 写一个 {choice:"skip"} 信号并跳进工作区。工作区外壳里的欢迎
 *     Hook 会在落地时创建一条「安装运行时」的引导 Issue。
 *   - 连上运行时：在选定的运行时上创建或修复该工作区的 Mika、发起一条隐藏的
 *     onboarding kickoff、标记引导完成，然后打开 Mika 的真实对话。不创建固定的
 *     specialist 团队。
 *
 * 本文件从不直接碰 createAgent / createIssue。「跳过运行时」的引导 Issue 逻辑在
 * `packages/views/workspace/welcome-after-onboarding.tsx`。
 */
interface OnboardingFlowProps {
  onComplete: (
    workspace?: Workspace,
    destination?: OnboardingDestination,
  ) => void;
  /** "new_workspace" 是同一个流程被已经在用 Multica 的人再次触发时的形态：
   *  它从工作区步开始——欢迎介绍和问卷每人只需要一次——并且总是新建工作区，
   *  而不是提供「继续用已有工作区」的选项。 */
  mode?: OnboardingMode;
  /** "new_workspace" 形态下必填：首次引导没有退出通道、只能登出；而给第二个
   *  工作区建号的用户必须能反悔、回到原来的地方。 */
  onCancel?: () => void;
  runtimeInstructions?: React.ReactNode;
}

/**
 * 首次使用引导入口组件：根据 `mode` 决定从欢迎页还是工作区页开始跑完整流程。
 */
export function OnboardingFlow(props: OnboardingFlowProps) {
  return <OnboardingStepFlow {...props} />;
}

/** 引导流程本体：按 mode 决定起始步骤，并编排各步骤的切换与提交。 */
function OnboardingStepFlow({
  onComplete,
  mode = "first_run",
  onCancel,
  runtimeInstructions,
}: OnboardingFlowProps) {
  const { t } = useT("onboarding");
  const user = useAuthStore((s) => s.user);
  if (!user) {
    throw new Error("OnboardingFlow requires an authenticated user");
  }

  // 问卷答案存在服务端、再次进入时会预填到各问卷步。这是引导状态里唯一跨会话
  // 保留的内容——「当前走到哪一步」刻意不保存，所以每次进入都从 Welcome 开始。
  const storedQuestionnaire = mergeQuestionnaire(user.onboarding_questionnaire);
  const [answers, setAnswers] = useState<QuestionnaireAnswers>(storedQuestionnaire);

  const isNewWorkspace = mode === "new_workspace";
  const [step, setStep] = useState<OnboardingStep>(
    isNewWorkspace ? "workspace" : "welcome",
  );
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  // 由正在发请求的那一步置位；会锁死「返回」按钮和进度栏。目前只有工作区步会置位。
  const [stepBusy, setStepBusy] = useState(false);
  const bootstrapMika = useBootstrapMika(workspace?.id ?? "");

  // 在 Welcome 步和第 2 步（workspace）都拉取工作区列表。第 2 步用它检测是否有
  // 早期「中途放弃的引导」留下的工作区（有的话 StepWorkspace 显示「继续使用 xxx」
  // 而不是新建表单——避免再建会撞 slug）。Welcome 步用它决定要不要渲染
  // 「我以前弄过」的跳过按钮——只有用户已经至少有一个工作区时才显示，否则跳过
  // 会让用户落到没有着落的地方。
  const { workspaces, ready: workspacesReady } = useWorkspaceList({
    enabled: step === "welcome" || step === "workspace",
  });
  const existingWorkspace = isNewWorkspace
    ? workspace
    : (workspace ?? workspaces[0] ?? null);
  const canSkipWelcome = workspacesReady && workspaces.length > 0;

  // 「当前步之后是哪一步」直接从 ONBOARDING_STEP_ORDER 推导，这样以后要增删
  // 某个持久步骤，只需编辑那份唯一数组。`from` 是最后一步或不在数组里时返回
  // null（调用方再各自决定路由）。
  const nextStep = useCallback((from: OnboardingStep): OnboardingStep | null => {
    const idx = ONBOARDING_STEP_ORDER.indexOf(from);
    if (idx < 0 || idx >= ONBOARDING_STEP_ORDER.length - 1) return null;
    return ONBOARDING_STEP_ORDER[idx + 1]!;
  }, []);

  const advanceFrom = useCallback(
    (from: OnboardingStep) => {
      const next = nextStep(from);
      if (next) setStep(next);
    },
    [nextStep],
  );

  const handleWelcomeNext = useCallback(() => {
    // Welcome 刻意不进 ONBOARDING_STEP_ORDER（它是产品介绍页，不是持久步骤），
    // 所以第一个持久步骤在这里写死作为入口。
    setStep(ONBOARDING_STEP_ORDER[0]!);
  }, []);

  // 先做内存态补丁，再发一个不用等的 PATCH 去持久化。绝不让请求阻塞 UI——
  // 下一屏渲染才是关键；偶发的保存失败只弹 toast，不会把用户回滚。
  const applyAnswers = useCallback(
    (patch: Partial<QuestionnaireAnswers>) => {
      setAnswers((a) => {
        const next = { ...a, ...patch };
        void saveQuestionnaire(next).catch((err) => {
          if (err instanceof Error) toast.error(err.message);
        });
        return next;
      });
    },
    [],
  );

  // 「我以前弄过」路径——老用户已有一个工作区、只想直接落到那里。先在服务端把
  // 引导标记为完成（用 COALESCE 保证幂等），然后导航过去，不新建任何工作区内容。
  const handleWelcomeSkip = useCallback(async () => {
    try {
      await completeOnboarding("skip_existing", workspaces[0]?.id);
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : t(($) => $.errors.skip_failed),
      );
      return;
    }
    onComplete(workspaces[0] ?? undefined);
  }, [workspaces, onComplete]);

  const handleWorkspaceCreated = useCallback(
    (ws: Workspace) => {
      setWorkspace(ws);
      // 刻意不调 setCurrentWorkspace：那个单例同时被桌面端标签页系统写，新工作区
      // 还没有标签组时它会被后者收走。跟它竞争会把后续整个流程——Mika、会话、
      // kickoff——带进之前活跃的工作区。所以从这里开始每一步都指名目标工作区，
      // 真正的切换只发生一次，就在 onComplete 里的那次导航上。
      advanceFrom("workspace");
    },
    [advanceFrom],
  );

  const handleRuntimeNext = useCallback(
    async (rt: AgentRuntime | null, model?: string) => {
      if (!workspace) return;
      // 连上运行时：只创建 Mika 并立刻打开那段真实的交互式引导对话。specialist
      // 留到以后、等成员的实际工作流确实需要时再创建。
      if (rt) {
        try {
          // 此前的问卷保存都是刻意乐观的。这里把最新快照冲刷一次，好让服务端
          // 生成的 kickoff 能读到可靠的 role/use-case 上下文，而不是跟上一次
          // PATCH 赛跑。
          await saveQuestionnaire(answers);
          const result = await bootstrapMika.mutateAsync({
            workspaceSlug: workspace.slug,
            runtimeId: rt.id,
            model,
            ...getMikaOnboarding(),
          });
          await completeOnboarding("full", workspace.id);
          onComplete(workspace, {
            kind: "chat",
            sessionId: result.chatSession.id,
          });
        } catch (err) {
          toast.error(
            err instanceof Error
              ? err.message
              : t(($) => $.errors.mika_setup_failed),
          );
          throw err;
        }
        return;
      }
      try {
        await completeOnboarding("runtime_skipped", workspace.id);
      } catch (err) {
        toast.error(
          err instanceof Error ? err.message : t(($) => $.errors.skip_failed),
        );
        return;
      }
      useWelcomeStore.getState().set({
        workspaceId: workspace.id,
        choice: "skip",
      });
      onComplete(workspace, undefined);
    },
    [answers, bootstrapMika, workspace, onComplete, t],
  );

  const handleBack = useCallback((from: OnboardingStep) => {
    // 新建工作区模式下 workspace 步是入口，所以它上面的「返回」是离开整个引导，
    // 而不是走回这个模式刻意跳过的步骤。只在工作区还不存在时可达——工作区一旦
    // 建立就再也没有回到这步的通道（原因见 runtimeStepBack 的注释）。
    if (isNewWorkspace && from === "workspace") {
      onCancel?.();
      return;
    }
    const idx = ONBOARDING_STEP_ORDER.indexOf(from);
    if (idx <= 0) {
      // 第一个持久步（关于你）返回时回到 Welcome。
      setStep("welcome");
      return;
    }
    const prev = ONBOARDING_STEP_ORDER[idx - 1]!;
    setStep(prev);
  }, [isNewWorkspace, onCancel]);

  // 工作区一旦存在就没有什么可取消的了，所以新建工作区模式在运行时步拿掉了
  // 「返回」：往回走会到 workspace 步，而那个步的返回按钮含义是「离开」——真离开
  // 会抛下一个既无运行时、又无 Mika、也无指引的工作区。剩下的两个出口都落到
  // 有头有尾的地方：Skip 走「跳过运行时」路径（建引导 Issue + 落进新工作区），
  // 继续则配置 Mika。
  // 「退出登录」放在进度栏底部而不是钉在窗口角落：钉在角落会超出版心范围、还
  // 会压到「返回」上面，看起来像第二行标题。
  const headerTrailing = <OnboardingLogoutButton inline />;

  const runtimeStepBack = isNewWorkspace
    ? undefined
    : () => handleBack("runtime");

  // 进度栏只允许往回走——前面的步骤渲染成链接、后面的渲染成纯文本，因为往前走
  // 必须跑当前步的校验和提交。新建工作区模式完全没有进度栏导航：它从 workspace 步
  // 进入，一旦那个工作区存在，身后每一步都已不存在。这和 runtimeStepBack 保证的
  // 不变量一致。
  const handleStepChange = isNewWorkspace ? undefined : setStep;

  // 整个流程只用一个外壳，在这里渲染而不是让每步自己渲染。以前每步都渲染自己的
  // <StepShell>，而每步组件类型不同，React 会在每次切换时拆掉旧外壳建新外壳——
  // 于是「常驻」进度栏被重挂载、画布动画重开、外壳还要从透明再淡入一次。那种整窗
  // 重淡入就是闪屏的来源。把外壳上提后进度栏才真正常驻，切换的只有步骤本体。
  if (step === "welcome") {
    // Welcome 没有进度栏，所以逃生出口在这里仍是钉在角落的。
    return (
      <>
        <OnboardingLogoutButton />
        <StepWelcome
          onNext={handleWelcomeNext}
          onSkip={canSkipWelcome ? handleWelcomeSkip : undefined}
        />
      </>
    );
  }

  const stepBack =
    step === "about_you"
      ? () => handleBack("about_you")
      : step === "workspace"
        ? () => handleBack("workspace")
        : runtimeStepBack;

  return (
    <StepShell
      currentStep={step}
      onBack={stepBack}
      backDisabled={stepBusy}
      onStepChange={handleStepChange}
      chromeFooter={headerTrailing}
    >
      {step === "about_you" && (
        <StepAboutYou
          answers={answers}
          onChange={applyAnswers}
          onAdvance={() => advanceFrom("about_you")}
          onSkip={() => advanceFrom("about_you")}
        />
      )}

      {step === "workspace" && (
        <StepWorkspace
          existing={existingWorkspace}
          onCreated={handleWorkspaceCreated}
          onBusyChange={setStepBusy}
        />
      )}

      {/* 第 3 步：要么通过 CLI 连上一个运行时（daemon），要么走云端等待列表 /
          跳过路径。 */}
      {step === "runtime" &&
        workspace &&
        <StepPlatformFork
          wsId={workspace.id}
          wsSlug={workspace.slug}
          onNext={handleRuntimeNext}
          cliInstructions={runtimeInstructions}
        />}
    </StepShell>
  );
}

/** 引导运行模式：`first_run` 首次使用全流程；`new_workspace` 老用户再建工作区。 */
export type OnboardingMode = "first_run" | "new_workspace";

/**
 * 引导完成后的跳转目的地：
 * - `issue`：跳到某条任务（引导 Issue）
 * - `chat`：跳到与某会话（如与 Mika）的聊天
 */
export type OnboardingDestination =
  | { kind: "issue"; issueId: string }
  | {
      kind: "chat";
      sessionId: string;
    };

export type { OnboardingStep };
