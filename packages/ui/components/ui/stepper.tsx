"use client"
// 本文件是分步流程（Stepper）组件族：把多步操作（向导/表单）的可点击步骤条
// 与各步内容面板组合起来。步骤触发区按 Tabs 的键盘约定（←/→、Home/End、
// Enter）导航；每一步可标记完成、禁用、加载等状态，并支持自定义状态图标。

import type { HTMLAttributes, ReactElement } from "react"
import {
  Children,
  createContext,
  isValidElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"

import { cn } from "@multica/ui/lib/utils"

// ---------- 类型 ----------
/** 步骤条排布方向 */
type StepperOrientation = "horizontal" | "vertical"
/** 单个步骤的四种状态 */
type StepState = "active" | "completed" | "inactive" | "loading"
/** 四种状态下允许自定义显示的图标（缺省则显示步骤号） */
type StepIndicators = {
  active?: React.ReactNode
  completed?: React.ReactNode
  inactive?: React.ReactNode
  loading?: React.ReactNode
}

/** Stepper 下发给各子组件的上下文 */
interface StepperContextValue {
  activeStep: number
  setActiveStep: (step: number) => void
  stepsCount: number
  orientation: StepperOrientation
  registerTrigger: (node: HTMLButtonElement | null) => void
  triggerNodes: HTMLButtonElement[]
  focusNext: (currentIdx: number) => void
  focusPrev: (currentIdx: number) => void
  focusFirst: () => void
  focusLast: () => void
  indicators: StepIndicators
}

/** StepperItem 下发给其内部子组件的上下文 */
interface StepItemContextValue {
  step: number
  state: StepState
  isDisabled: boolean
  isLoading: boolean
}

const StepperContext = createContext<StepperContextValue | undefined>(undefined)
const StepItemContext = createContext<StepItemContextValue | undefined>(
  undefined
)

/** 读取 Stepper 上下文，必须在 <Stepper> 内使用 */
function useStepper() {
  const ctx = useContext(StepperContext)
  if (!ctx) throw new Error("useStepper must be used within a Stepper")
  return ctx
}

/** 读取 StepItem 上下文，必须在 <StepperItem> 内使用 */
function useStepItem() {
  const ctx = useContext(StepItemContext)
  if (!ctx) throw new Error("useStepItem must be used within a StepperItem")
  return ctx
}

/** Stepper 组件的 props 定义 */
interface StepperProps extends HTMLAttributes<HTMLDivElement> {
  /** 非受控模式下的初始激活步骤（从 1 开始），默认 1 */
  defaultValue?: number
  /** 受控模式下的激活步骤 */
  value?: number
  /** 激活步骤变化回调 */
  onValueChange?: (value: number) => void
  /** 方向：水平（默认，步骤横排）/ 垂直 */
  orientation?: StepperOrientation
  /** 四种状态下可替换的指示图标 */
  indicators?: StepIndicators
}

/**
 * Stepper：分步流程根组件。以 role="tablist" 呈现，负责维护当前激活步骤、
 * 统计步骤数，并把键盘导航能力下发给每个触发按钮。
 */
function Stepper({
  defaultValue = 1,
  value,
  onValueChange,
  orientation = "horizontal",
  className,
  children,
  indicators = {},
  ...props
}: StepperProps) {
  const [activeStep, setActiveStep] = useState(defaultValue)
  const [triggerNodes, setTriggerNodes] = useState<HTMLButtonElement[]>([])

  // 注册/注销步骤触发按钮：把已挂载的按钮收集起来供键盘导航轮转
  const registerTrigger = useCallback((node: HTMLButtonElement | null) => {
    setTriggerNodes((prev) => {
      if (node && !prev.includes(node)) {
        return [...prev, node]
      } else if (!node && prev.includes(node!)) {
        return prev.filter((n) => n !== node)
      } else {
        return prev
      }
    })
  }, [])

  const handleSetActiveStep = useCallback(
    (step: number) => {
      // 受控时不动内部 state，只把变更上报
      if (value === undefined) {
        setActiveStep(step)
      }
      onValueChange?.(step)
    },
    [value, onValueChange]
  )

  const currentStep = value ?? activeStep

  // ---------- 键盘导航 ----------
  const focusTrigger = (idx: number) => {
    if (triggerNodes[idx]) triggerNodes[idx].focus()
  }
  // 焦点在触发按钮间环形移动
  const focusNext = (currentIdx: number) =>
    focusTrigger((currentIdx + 1) % triggerNodes.length)
  const focusPrev = (currentIdx: number) =>
    focusTrigger((currentIdx - 1 + triggerNodes.length) % triggerNodes.length)
  const focusFirst = () => focusTrigger(0)
  const focusLast = () => focusTrigger(triggerNodes.length - 1)

  // 汇总下发给子组件的上下文
  const contextValue = useMemo<StepperContextValue>(
    () => ({
      activeStep: currentStep,
      setActiveStep: handleSetActiveStep,
      stepsCount: Children.toArray(children).filter(
        (child): child is ReactElement =>
          isValidElement(child) &&
          (child.type as { displayName?: string }).displayName === "StepperItem"
      ).length,
      orientation,
      registerTrigger,
      focusNext,
      focusPrev,
      focusFirst,
      focusLast,
      triggerNodes,
      indicators,
    }),
    [
      currentStep,
      handleSetActiveStep,
      children,
      orientation,
      registerTrigger,
      triggerNodes,
    ]
  )

  return (
    <StepperContext.Provider value={contextValue}>
      <div
        role="tablist"
        aria-orientation={orientation}
        data-slot="stepper"
        className={cn("w-full", className)}
        data-orientation={orientation}
        {...props}
      >
        {children}
      </div>
    </StepperContext.Provider>
  )
}

/** StepperItem 组件的 props 定义 */
interface StepperItemProps extends React.HTMLAttributes<HTMLDivElement> {
  /** 本步骤的序号（从 1 开始，对应激活值） */
  step: number
  /** 强制标记为已完成 */
  completed?: boolean
  /** 禁用本步骤（不可点击） */
  disabled?: boolean
  /** 标记加载中（仅激活的步骤会进入 loading 状态） */
  loading?: boolean
}

/**
 * StepperItem：单个步骤节点。根据 step 与激活值自动推导状态：
 * completed 显式传 true 或 step 已过 → completed；等于激活值 → active；
 * 其余 → inactive。loading 且为本步时进入 loading 态。
 */
function StepperItem({
  step,
  completed = false,
  disabled = false,
  loading = false,
  className,
  children,
  ...props
}: StepperItemProps) {
  const { activeStep } = useStepper()

  const state: StepState =
    completed || step < activeStep
      ? "completed"
      : activeStep === step
        ? "active"
        : "inactive"

  const isLoading = loading && step === activeStep

  return (
    <StepItemContext.Provider
      value={{ step, state, isDisabled: disabled, isLoading }}
    >
      <div
        data-slot="stepper-item"
        className={cn(
          "group/step flex items-center justify-center not-last:flex-1 group-data-[orientation=horizontal]/stepper-nav:flex-row group-data-[orientation=vertical]/stepper-nav:flex-col",
          className
        )}
        data-state={state}
        {...(isLoading ? { "data-loading": true } : {})}
        {...props}
      >
        {children}
      </div>
    </StepItemContext.Provider>
  )
}

/** StepperTrigger 的 props：useRender 的 button 组件 props */
type StepperTriggerProps = useRender.ComponentProps<"button">

/**
 * StepperTrigger：步骤按钮。带完整的键盘交互：←/→（水平）与 ↑/↓（垂直）
 * 在步骤间移动焦点，Home/End 到首/末，Enter/空格激活本步。已激活步骤
 * tabIndex=0 可 Tab 到，其余 -1。
 */
function StepperTrigger({
  className,
  children,
  tabIndex,
  render,
  ...props
}: StepperTriggerProps) {
  const { state, isLoading } = useStepItem()
  const stepperCtx = useStepper()
  const {
    setActiveStep,
    activeStep,
    registerTrigger,
    triggerNodes,
    focusNext,
    focusPrev,
    focusFirst,
    focusLast,
  } = stepperCtx
  const { step, isDisabled } = useStepItem()
  const isSelected = activeStep === step
  const id = `stepper-tab-${step}`
  const panelId = `stepper-panel-${step}`

  // 注册本按钮，供键盘导航轮转
  const btnRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (btnRef.current) {
      registerTrigger(btnRef.current)
    }
  }, [btnRef.current])

  // 在所有触发按钮里找自己的下标，用于方向键移动
  const myIdx = useMemo(
    () =>
      triggerNodes.findIndex((n: HTMLButtonElement) => n === btnRef.current),
    [triggerNodes, btnRef.current]
  )

  const handleKeyDown = (e: React.KeyboardEvent<HTMLButtonElement>) => {
    switch (e.key) {
      case "ArrowRight":
      case "ArrowDown":
        e.preventDefault()
        if (myIdx !== -1 && focusNext) focusNext(myIdx)
        break
      case "ArrowLeft":
      case "ArrowUp":
        e.preventDefault()
        if (myIdx !== -1 && focusPrev) focusPrev(myIdx)
        break
      case "Home":
        e.preventDefault()
        if (focusFirst) focusFirst()
        break
      case "End":
        e.preventDefault()
        if (focusLast) focusLast()
        break
      case "Enter":
      case " ":
        e.preventDefault()
        setActiveStep(step)
        break
    }
  }

  const defaultProps = {
    role: "tab",
    id,
    "aria-selected": isSelected,
    "aria-controls": panelId,
    tabIndex: typeof tabIndex === "number" ? tabIndex : isSelected ? 0 : -1,
    "data-slot": "stepper-trigger",
    "data-state": state,
    "data-loading": isLoading,
    className: cn(
      "focus-visible:border-ring focus-visible:ring-ring/50 inline-flex cursor-pointer items-center outline-none focus-visible:z-10 focus-visible:ring-3 disabled:pointer-events-none disabled:opacity-60",
      "gap-2.5 rounded-full",
      className
    ),
    onClick: () => setActiveStep(step),
    onKeyDown: handleKeyDown,
    disabled: isDisabled,
    children,
  }

  return useRender({
    defaultTagName: "button",
    render,
    ref: btnRef,
    props: mergeProps<"button">(defaultProps, props),
  })
}

/** StepperIndicator：步骤左侧的圆形指示图标；可用 indicators 覆盖各状态图标 */
function StepperIndicator({
  children,
  className,
}: React.ComponentProps<"div">) {
  const { state, isLoading } = useStepItem()
  const { indicators } = useStepper()

  return (
    <div
      data-slot="stepper-indicator"
      data-state={state}
      className={cn(
        "border-background bg-accent text-accent-foreground data-[state=completed]:bg-primary data-[state=completed]:text-primary-foreground data-[state=active]:bg-primary data-[state=active]:text-primary-foreground relative flex size-6 shrink-0 items-center justify-center overflow-hidden",
        "rounded-full text-caption",
        className
      )}
    >
      <div className="absolute">
        {indicators &&
        ((isLoading && indicators.loading) ||
          (state === "completed" && indicators.completed) ||
          (state === "active" && indicators.active) ||
          (state === "inactive" && indicators.inactive))
          ? (isLoading && indicators.loading) ||
            (state === "completed" && indicators.completed) ||
            (state === "active" && indicators.active) ||
            (state === "inactive" && indicators.inactive)
          : children}
      </div>
    </div>
  )
}

/** StepperSeparator：步骤之间的连接线（水平是横线、垂直是竖线） */
function StepperSeparator({ className }: React.ComponentProps<"div">) {
  const { state } = useStepItem()

  return (
    <div
      data-slot="stepper-separator"
      data-state={state}
      className={cn(
        "bg-muted rounded-sm m-0.5 group-data-[orientation=horizontal]/stepper-nav:h-0.5 group-data-[orientation=horizontal]/stepper-nav:flex-1 group-data-[orientation=vertical]/stepper-nav:h-12 group-data-[orientation=vertical]/stepper-nav:w-0.5",
        className
      )}
    />
  )
}

/** StepperTitle：步骤标题文字 */
function StepperTitle({ children, className }: React.ComponentProps<"h3">) {
  const { state } = useStepItem()

  return (
    <h3
      data-slot="stepper-title"
      data-state={state}
      className={cn("text-label font-medium", className)}
    >
      {children}
    </h3>
  )
}

/** StepperDescription：步骤下方的说明文字 */
function StepperDescription({
  children,
  className,
}: React.ComponentProps<"div">) {
  const { state } = useStepItem()

  return (
    <div
      data-slot="stepper-description"
      data-state={state}
      className={cn("text-muted-foreground text-caption", className)}
    >
      {children}
    </div>
  )
}

/** StepperNav：装步骤节点的导航容器（就是步骤条本体） */
function StepperNav({ children, className }: React.ComponentProps<"nav">) {
  const { activeStep, orientation } = useStepper()

  return (
    <nav
      data-slot="stepper-nav"
      data-state={activeStep}
      data-orientation={orientation}
      className={cn(
        "group/stepper-nav inline-flex data-[orientation=horizontal]:w-full data-[orientation=horizontal]:flex-row data-[orientation=vertical]:flex-col",
        className
      )}
    >
      {children}
    </nav>
  )
}

/** StepperPanel：与步骤条配套的内容面板容器 */
function StepperPanel({ children, className }: React.ComponentProps<"div">) {
  const { activeStep } = useStepper()

  return (
    <div
      data-slot="stepper-panel"
      data-state={activeStep}
      className={cn("w-full", className)}
    >
      {children}
    </div>
  )
}

/** StepperContent 的 props 定义 */
interface StepperContentProps extends React.ComponentProps<"div"> {
  /** 对应哪一步的内容 */
  value: number
  /** 为 true 时即使不是激活步骤也保持挂载（用 hidden 隐藏而非卸载） */
  forceMount?: boolean
}

/**
 * StepperContent：某一步的内容区。非激活步骤默认卸载；
 * forceMount 时保留在 DOM 里只是隐藏，适合需要在切换间保持状态的步骤。
 */
function StepperContent({
  value,
  forceMount,
  children,
  className,
}: StepperContentProps) {
  const { activeStep } = useStepper()
  const isActive = value === activeStep

  if (!forceMount && !isActive) {
    return null
  }

  return (
    <div
      data-slot="stepper-content"
      data-state={activeStep}
      className={cn("w-full", className, !isActive && forceMount && "hidden")}
      hidden={!isActive && forceMount}
    >
      {children}
    </div>
  )
}

export {
  useStepper,
  useStepItem,
  Stepper,
  StepperItem,
  StepperTrigger,
  StepperIndicator,
  StepperSeparator,
  StepperTitle,
  StepperDescription,
  StepperPanel,
  StepperContent,
  StepperNav,
  type StepperProps,
  type StepperItemProps,
  type StepperTriggerProps,
  type StepperContentProps,
}