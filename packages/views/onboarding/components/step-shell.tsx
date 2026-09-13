"use client";

// 引导流程每一步的通用外壳：左侧是进度侧栏（窄屏换成顶部进度条），右侧是
// 该步骤自己的内容区，负责把各步骤重复的窗口骨架收敛到这一处。
import { useRef, type ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import { DragStrip } from "@multica/views/platform";
import type { OnboardingStep } from "@multica/core/onboarding";
import { StepProgressBar, StepSidebar } from "./step-sidebar";

/**
 * 引导步骤的版心尺寸，取自 ReUI 的 onboarding-3 区块，让左右两个面板读起来
 * 像是同一次设计。
 *
 * 旧方案有三种互相竞争的宽度——920px 的外框、620px 的列、框内还有一个上限——
 * 结果平台分叉那一步比其他步骤向左偏出约 150px。现在全流程只有一个宽度标准；
 * 某个步骤真需要跳出版心时，就在它自己的文件里说明，而不是另选一个全局尺寸。
 */

/** 步骤内容的纵向列：全宽容器内居中、宽度封顶 28rem、按列方向排布。 */
export const STEP_COLUMN = "mx-auto flex min-h-full w-full max-w-[28rem] flex-col";
/** 步骤内容区的外边距（内边距），随宽度档位变化。 */
export const STEP_GUTTER = "px-6 py-8 sm:px-10 lg:px-14 lg:py-10";

/**
 * 一个步骤的标题 + 辅助说明行。
 *
 * 它取代了「灰色大写眉题 + 衬线大字标题」的组合。现在区块直接用普通半粗无衬线
 * 标题开头、下面跟一行灰色说明，不再有眉题——既然左侧进度栏已经说明了当前是
 * 哪一步，再给同一个画面加第三个标签就是冗余。
 *
 * 加 `aria-live` 是因为两侧面板在步骤切换时保持不变：读屏用户在各步之间移动时
 * 收不到任何导航事件，标题必须自己播报出来。
 *
 * @param title 标题内容
 * @param description 标题下方的辅助说明（可选）
 */
export function StepHeading({
  title,
  description,
}: {
  title: ReactNode;
  description?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5" aria-live="polite">
      <h1 className="text-balance text-title-lg font-semibold text-foreground">
        {title}
      </h1>
      {description ? (
        <p className="text-pretty text-body text-muted-foreground">
          {description}
        </p>
      ) : null}
    </div>
  );
}

/**
 * 步骤的操作区，钉在整列的底部。
 *
 * 用「通栏、竖向堆叠」的按钮而不是右对齐的一排横排按钮：列宽只有 28rem，
 * 横排放置会让主按钮浮在屏幕中间，而不是落在用户视线完成表单的位置。
 * `mt-auto` 配合列的 `min-h-full` 就是把它压到最底下的机制。
 *
 * @param children 按钮组内容
 * @param hint 按钮上方的可选状态行（校验提示、进度等）
 */
export function StepFooter({
  children,
  hint,
}: {
  children: ReactNode;
  /** 按钮上方可选的状态行（校验提示、进度等）。 */
  hint?: ReactNode;
}) {
  return (
    <div className="mt-auto flex flex-col gap-2 pb-2 pt-10">
      {hint ? (
        <p aria-live="polite" className="text-caption text-muted-foreground">
          {hint}
        </p>
      ) : null}
      {children}
    </div>
  );
}

/**
 * 每个引导步骤渲染进的外壳：左侧是常驻进度栏，右侧是该步骤自己的可滚动内容。
 *
 * 它拥有的是整个窗口，而不只是一条头部栏——正是这一点让四个步骤不用再各自重复
 * 一套相同的 wrapper / DragStrip / 头部 / `<main>` 前缀，包括之前四个文件里原样
 * 复制了四遍的滚动渐隐接线，现在只在这里装配一次。
 *
 * 列用的是滚动面板里的 `min-h-full`，而不是由面板居中：给滚动容器加 `items-center`
 * 会把任何比视口高的内容顶部裁掉，而这些步骤在矮窗口里确实会超高。
 *
 * @param currentStep 当前所在步骤
 * @param onBack 返回上一步的回调（缺省则不显示返回按钮）
 * @param backDisabled 是否禁用返回（工作区步骤在创建请求进行中会禁用）
 * @param onStepChange 从进度栏回到某个已完成步骤的回调
 * @param chromeFooter 由外层流程注入的「退出登录」出口，渲染在当前可见的
 *   chrome 上：md 及以上在进度栏里，以下在顶部压缩栏里
 * @param children 该步骤自己的内容
 */
export function StepShell({
  currentStep,
  onBack,
  backDisabled,
  onStepChange,
  chromeFooter,
  children,
}: {
  currentStep: OnboardingStep;
  onBack?: () => void;
  /** 是否禁用「返回」——工作区步骤在创建请求进行中会禁用。 */
  backDisabled?: boolean;
  /** 从进度栏直接回到某个已完成步骤的回调。 */
  onStepChange?: (step: OnboardingStep) => void;
  /** 由流程注入的「退出登录」出口：渲染在当前可见的 chrome 上——
   *  md 及以上在进度栏里，以下在顶部压缩栏里。 */
  chromeFooter?: ReactNode;
  children: ReactNode;
}) {
  const mainRef = useRef<HTMLElement>(null);
  const fadeStyle = useScrollFade(mainRef);

  return (
    <div className="animate-onboarding-enter flex h-full min-h-0 flex-col bg-background">
      {/* 整个窗口只放一条拖拽条、并让它当第一个 flex 子元素——这既是桌面端外壳
          规则要求的形状，也是修一个重叠问题：进度栏是深色内嵌面板，从窗口左上角
          开始铺，而 macOS 恰好把红绿灯按钮画在左上角，于是两者叠在了一起。在
          两个面板上方预留这条条带，就能让面板避开红绿灯，同时整条带都可拖拽。
          以前用两条各管一块的条带拼这件事，两条都不是第一个子元素。 */}
      <DragStrip />

      <div className="flex min-h-0 flex-1">
        <StepSidebar
          currentStep={currentStep}
          onBack={onBack}
          backDisabled={backDisabled}
          onStepChange={onStepChange}
          footer={chromeFooter}
        />

        <main
          ref={mainRef}
          style={fadeStyle}
          className={cn("min-h-0 min-w-0 flex-1 overflow-y-auto", STEP_GUTTER)}
        >
          <div className={STEP_COLUMN}>
            <StepProgressBar
              currentStep={currentStep}
              onBack={onBack}
              backDisabled={backDisabled}
              footer={chromeFooter}
            />
            {children}
          </div>
        </main>
      </div>
    </div>
  );
}
