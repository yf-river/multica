"use client"
// 本文件是应用侧边栏（Sidebar）组件族（shadcn 风格，基于 Base UI）：
// 桌面端是常驻侧栏（可折叠为纯图标、可拖宽、可浮出/嵌入内容区），
// 窄屏时退化为从侧边滑出的抽屉（Sheet）。内部按「菜单组 → 菜单项 → 子菜单」
// 的结构拼装导航。

import * as React from "react"
import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"
import { cva, type VariantProps } from "class-variance-authority"
import { useTranslation } from "react-i18next"

import { useIsCompact } from "@multica/ui/hooks/use-mobile"
import { cn } from "@multica/ui/lib/utils"
import { Button } from "@multica/ui/components/ui/button"
import { Input } from "@multica/ui/components/ui/input"
import { Separator } from "@multica/ui/components/ui/separator"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet"
import { Skeleton } from "@multica/ui/components/ui/skeleton"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip"
import { PanelLeftIcon } from "lucide-react"

/** 侧栏默认宽度（px） */
const SIDEBAR_WIDTH_DEFAULT = 256
/** 侧栏可拖出的最小宽度 */
const SIDEBAR_WIDTH_MIN = 200
/** 侧栏可拖出的最大宽度 */
const SIDEBAR_WIDTH_MAX = 360
/** 拖宽结果存 localStorage 用的键名 */
const SIDEBAR_WIDTH_STORAGE_KEY = "sidebar_width"
/** 移动端抽屉里侧栏占用的宽度 */
const SIDEBAR_WIDTH_MOBILE = "18rem"
/** 纯图标（折叠）模式下的宽度 */
const SIDEBAR_WIDTH_ICON = "3rem"
/** 指针位移超过该值才算一次拖宽（避免点一下也改变宽度） */
const SIDEBAR_DRAG_THRESHOLD = 2
// Tailwind 的 `lg`–`xl` 段：宽到能做「列表 + 详情」双栏，但没宽到值得再花
// 256px 放导航。这段宽度里导航默认折叠，靠顶栏的触发器重新展开。
const SIDEBAR_AUTO_COLLAPSE_QUERY = "(min-width: 1024px) and (max-width: 1279px)"

/**
 * 让后代元素用「侧栏包装层当前实际填充的颜色」来上色。
 *
 * 某个后代如果要盖一层不透明底在包装层上——而不是盖在自己父级上——就必须
 * 与包装层的填充色完全一致，而这个填充是条件性的：嵌中式（inset）侧栏挂载时
 * 是 `bg-sidebar`，否则是消费者自己的背景。在后代里直接点名某个 token，
 * 等于把那个条件复制到了第二处，两处迟早分叉（#6874）。改用这个 class：
 * 包装层在同一个 `:has()` 条件（既负责上色也负责发布）下把填充发布为
 * `--sidebar-wrapper-fill`，两者永远不会不一致。
 *
 * 给包装层自定义背景的消费者必须声明对应的非 inset 半段，
 * 例如 `bg-app-shell [--sidebar-wrapper-fill:var(--app-shell)]`。
 */
const SIDEBAR_WRAPPER_FILL_CLASS = "bg-(--sidebar-wrapper-fill)"

/** 把宽度夹到 [min, max] 区间 */
function clampSidebarWidth(width: number) {
  return Math.max(SIDEBAR_WIDTH_MIN, Math.min(SIDEBAR_WIDTH_MAX, width))
}

/** Sidebar 上下文提供的内容 */
type SidebarContextProps = {
  state: "expanded" | "collapsed"
  open: boolean
  setOpen: (open: boolean) => void
  openMobile: boolean
  setOpenMobile: (open: boolean) => void
  /**
   * 当前是否处于「紧凑」形态——侧栏是覆盖式抽屉，而不是页面流里的一列。
   * 紧凑断点以下为 true：它覆盖平板和可折叠内屏，不只是手机。
   */
  isCompact: boolean
  toggleSidebar: () => void
  /**
   * 外壳（app shell）是否在页面 chrome 之外自带一个永远够得着的
   * `SidebarTrigger`——桌面窗口工具栏就有，在红绿灯按钮旁边。
   *
   * 那些本来要提供兜底触发器的界面读到这个标记后就什么也不提供：外壳自己的
   * 触发器永远可用，再造一个只是重复的 chrome，而不是「给没有导航入口的导航
   * 一个入口」（MUL-6218）。它描述的是外壳，不是当前视口，所以不随那个触发器
   * 本次是否真的挂载而变化。
   */
  hasExternalTrigger: boolean
}

/** 侧栏拖宽上下文的内容 */
type SidebarResizeContextProps = {
  commitWidth: (width: number) => void
}

const SidebarContext = React.createContext<SidebarContextProps | null>(null)
// 拖宽过程中宽度预览是直接写到两个布局壳上的；这个 context 只在松手时暴露
// 那一次「提交」状态变更。
const SidebarResizeContext = React.createContext<SidebarResizeContextProps | null>(null)

/** 读取 Sidebar 上下文；不在 <SidebarProvider> 内使用会报错 */
function useSidebar() {
  const context = React.use(SidebarContext)
  if (!context) {
    throw new Error("useSidebar must be used within a SidebarProvider.")
  }

  return context
}

/** 读取 Sidebar 上下文的安全版本：不在 Provider 内时返回 null 而不是抛错 */
function useSidebarSafe() {
  return React.use(SidebarContext)
}

/** 读取侧栏拖宽上下文；不在 Provider 内使用会报错 */
function useSidebarResize() {
  const context = React.use(SidebarResizeContext)
  if (!context) {
    throw new Error("useSidebarResize must be used within a SidebarProvider.")
  }

  return context
}

/**
 * SidebarProvider：侧栏的根 Provider。管理开/合状态、移动端抽屉开关、
 * 自动折叠区间的进出、以及宽度（默认/持久化/拖宽提交）。
 */
function SidebarProvider({
  defaultOpen = true,
  open: openProp,
  onOpenChange: setOpenProp,
  hasExternalTrigger = false,
  className,
  style,
  children,
  ...props
}: React.ComponentProps<"div"> & {
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
  /**
   * 声明外壳在页面 chrome 之外自带一个永远够得着的 `SidebarTrigger`。
   * 见 SidebarContextProps.hasExternalTrigger。
   *
   * 默认 false 是安全的选择：忘了退出可选标记的外壳，最多多显示一个图标；
   * 而需要显式开启却忘了的外壳，会让折叠后的导航没有返回入口。
   */
  hasExternalTrigger?: boolean
}) {
  const isCompact = useIsCompact()
  const [openMobile, setOpenMobile] = React.useState(false)

  const [width, _setWidth] = React.useState(SIDEBAR_WIDTH_DEFAULT)
  React.useEffect(() => {
    const stored = localStorage.getItem(SIDEBAR_WIDTH_STORAGE_KEY)
    if (stored) {
      const storedWidth = Number(stored)
      if (Number.isFinite(storedWidth)) {
        _setWidth(clampSidebarWidth(storedWidth))
      }
    }
  }, [])
  const commitWidth = React.useCallback((w: number) => {
    const clamped = clampSidebarWidth(w)
    _setWidth(clamped)
    localStorage.setItem(SIDEBAR_WIDTH_STORAGE_KEY, String(clamped))
  }, [])

  // 这是侧栏的内部开关状态。openProp / setOpenProp 用于外部受控。
  //
  // 刻意不做持久化。上游 shadcn 在这里写一个 `sidebar_state` cookie 并在
  // 服务端用它对 defaultOpen 播种；我们删掉了写入，因为没有东西会读它，
  // 而且恢复这个对反而会是 bug 而非特性：下面的自动折叠会在进入 lg–xl 区间
  // 时驱动 setOpen(false)，把它的结果持久化，等于把用户没要求的折叠带出这个
  // 区间、带进下一次更宽屏幕上的会话。要持久化就需要能区分「用户手动开合」
  // 与「自动开合」的数据来源——这不是这个回调上一个 cookie 能解决的。
  const [_open, _setOpen] = React.useState(defaultOpen)
  const open = openProp ?? _open
  const setOpen = React.useCallback(
    (value: boolean | ((value: boolean) => boolean)) => {
      const openState = typeof value === "function" ? value(open) : value
      if (setOpenProp) {
        setOpenProp(openState)
      } else {
        _setOpen(openState)
      }
    },
    [setOpenProp, open]
  )

  // 在 lg–xl 宽度区间里自动折叠。只有两次「跨界」是自动的：进入时把我们
  // 带来的状态暂存并收起导航，离开时恢复暂存的状态。中间用户做的任何事——
  // 尤其是重新展开导航——都会被保留，直到下一次跨界。
  //
  // `setOpen` 的身份随 `open` 变化，所以监听器把两者都放进 ref 里读取，
  // 而不在每次开关时重订阅。ref 在首次渲染时播种、每次提交后刷新——声明在
  // 监听器上方，保证该 effect 首次运行时它已经是最新的。
  const latest = React.useRef({ open, setOpen })
  React.useEffect(() => {
    latest.current = { open, setOpen }
  })
  const parkedOpenRef = React.useRef<boolean | null>(null)
  React.useEffect(() => {
    const mql = window.matchMedia(SIDEBAR_AUTO_COLLAPSE_QUERY)
    const apply = (matches: boolean) => {
      if (matches) {
        if (parkedOpenRef.current !== null) return
        parkedOpenRef.current = latest.current.open
        latest.current.setOpen(false)
      } else if (parkedOpenRef.current !== null) {
        latest.current.setOpen(parkedOpenRef.current)
        parkedOpenRef.current = null
      }
    }
    apply(mql.matches)
    const onChange = (e: MediaQueryListEvent) => apply(e.matches)
    mql.addEventListener("change", onChange)
    return () => mql.removeEventListener("change", onChange)
  }, [])

  // 开/合切换：紧凑形态切移动端抽屉，桌面形态切常驻侧栏
  const toggleSidebar = React.useCallback(() => {
    return isCompact ? setOpenMobile((open) => !open) : setOpen((open) => !open)
  }, [isCompact, setOpen, setOpenMobile])

  // 这个 state 用来输出 data-state="expanded" / "collapsed"，
  // 方便直接用 Tailwind 类给侧栏不同形态写样式。
  const state = open ? "expanded" : "collapsed"

  const contextValue = React.useMemo<SidebarContextProps>(
    () => ({
      state,
      open,
      setOpen,
      isCompact,
      openMobile,
      setOpenMobile,
      toggleSidebar,
      hasExternalTrigger,
    }),
    [
      state,
      open,
      setOpen,
      isCompact,
      openMobile,
      setOpenMobile,
      toggleSidebar,
      hasExternalTrigger,
    ]
  )
  const resizeContextValue = React.useMemo<SidebarResizeContextProps>(
    () => ({
      commitWidth,
    }),
    [commitWidth]
  )

  return (
    <SidebarContext.Provider value={contextValue}>
      <SidebarResizeContext.Provider value={resizeContextValue}>
        <div
          data-slot="sidebar-wrapper"
          style={
            {
              "--sidebar-width": `${width}px`,
              "--sidebar-width-icon": SIDEBAR_WIDTH_ICON,
              ...style,
            } as React.CSSProperties
          }
          className={cn(
            "group/sidebar-wrapper flex min-h-svh w-full",
            // 嵌中式分支的两半共用同一个 :has() 条件，所以
            // SIDEBAR_WRAPPER_FILL_CLASS 永远不可能点到这个元素实际没在用的
            // 颜色。非 inset 的那半填充属于消费者（此时本组件不上任何色），
            // 所以消费者要在自己的背景类旁边声明那一半变量。
            "has-data-[variant=inset]:bg-sidebar has-data-[variant=inset]:[--sidebar-wrapper-fill:var(--sidebar)]",
            className
          )}
          {...props}
        >
          {children}
        </div>
      </SidebarResizeContext.Provider>
    </SidebarContext.Provider>
  )
}

/**
 * Sidebar：侧栏本体。三种 variant：sidebar 常规贴边 / floating 悬浮 / inset
 * 嵌入主区；collapsible 控制折叠方式（offcanvas 收起后完全移出 / icon 收起为
 * 图标条 / none 不可折叠）。窄屏（isCompact）时渲染成从侧边滑出的 Sheet。
 */
function Sidebar({
  side = "left",
  variant = "sidebar",
  collapsible = "offcanvas",
  className,
  children,
  dir,
  ...props
}: React.ComponentProps<"div"> & {
  side?: "left" | "right"
  variant?: "sidebar" | "floating" | "inset"
  collapsible?: "offcanvas" | "icon" | "none"
}) {
  const { isCompact, state, openMobile, setOpenMobile } = useSidebar()

  if (collapsible === "none") {
    return (
      <div
        data-slot="sidebar"
        className={cn(
          "flex h-full w-(--sidebar-width) flex-col bg-sidebar text-sidebar-foreground",
          className
        )}
        {...props}
      >
        {children}
      </div>
    )
  }

  if (isCompact) {
    return (
      <Sheet open={openMobile} onOpenChange={setOpenMobile} {...props}>
        <SheetContent
          dir={dir}
          data-sidebar="sidebar"
          data-slot="sidebar"
          data-mobile="true"
          className="w-(--sidebar-width) bg-sidebar p-0 text-sidebar-foreground [&>button]:hidden"
          style={
            {
              "--sidebar-width": SIDEBAR_WIDTH_MOBILE,
            } as React.CSSProperties
          }
          side={side}
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Sidebar</SheetTitle>
            <SheetDescription>Displays the mobile sidebar.</SheetDescription>
          </SheetHeader>
          <div className="flex h-full w-full flex-col">{children}</div>
        </SheetContent>
      </Sheet>
    )
  }

  // 用 `lg` 而不是 `md`：这条 CSS 门槛必须和上方选「常驻栏」还是「抽屉」的
  // useIsCompact() 门槛保持一致。当初两者不一致时，位于两个断点之间的每次
  // 加载都会先画一帧 256px 侧栏，等 hook 解析完才把它收成抽屉。
  return (
    <div
      className="group peer hidden text-sidebar-foreground lg:block"
      data-state={state}
      data-collapsible={state === "collapsed" ? collapsible : ""}
      data-variant={variant}
      data-side={side}
      data-slot="sidebar"
    >
      {/* 这个元素负责在桌面端占住侧栏让出的空隙 */}
      <div
        data-slot="sidebar-gap"
        className={cn(
          "relative w-(--sidebar-width) bg-transparent transition-[width] duration-200 ease-out motion-reduce:transition-none",
          "group-data-[collapsible=offcanvas]:w-0",
          "group-data-[side=right]:rotate-180",
          variant === "floating" || variant === "inset"
            ? "group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(4)))]"
            : "group-data-[collapsible=icon]:w-(--sidebar-width-icon)"
        )}
      />
      <div
        data-slot="sidebar-container"
        data-side={side}
        className={cn(
          "fixed inset-y-0 z-10 hidden h-svh w-(--sidebar-width) transition-[left,right,width] duration-200 ease-out motion-reduce:transition-none lg:flex",
          "data-[side=left]:left-0 data-[side=left]:group-data-[collapsible=offcanvas]:left-[calc(var(--sidebar-width)*-1)] data-[side=right]:right-0 data-[side=right]:group-data-[collapsible=offcanvas]:right-[calc(var(--sidebar-width)*-1)]",
          // 浮出式/嵌中式需要额外留一圈内边距
          variant === "floating" || variant === "inset"
            ? "p-2 group-data-[collapsible=icon]:w-[calc(var(--sidebar-width-icon)+(--spacing(4))+2px)]"
            : "group-data-[collapsible=icon]:w-(--sidebar-width-icon) group-data-[side=left]:border-r group-data-[side=right]:border-l",
          className
        )}
        {...props}
      >
        <div
          data-sidebar="sidebar"
          data-slot="sidebar-inner"
          className="flex size-full flex-col bg-sidebar group-data-[variant=floating]:rounded-lg group-data-[variant=floating]:shadow-sm group-data-[variant=floating]:ring-1 group-data-[variant=floating]:ring-sidebar-border"
        >
          {children}
        </div>
      </div>
    </div>
  )
}

/** SidebarTrigger：顶栏/页面上切换侧栏开合的按钮（面板图标） */
function SidebarTrigger({
  className,
  onClick,
  ...props
}: React.ComponentProps<typeof Button>) {
  const { toggleSidebar } = useSidebar()
  const { t } = useTranslation("ui")

  return (
    <Button
      data-sidebar="trigger"
      data-slot="sidebar-trigger"
      variant="ghost"
      size="icon-sm"
      className={cn(className)}
      onClick={(event) => {
        onClick?.(event)
        toggleSidebar()
      }}
      {...props}
    >
      <PanelLeftIcon />
      <span className="sr-only">{t(($) => $.toggle_sidebar)}</span>
    </Button>
  )
}

/**
 * SidebarRail：侧栏右边沿的细拖拽条。可以拖它调整侧栏宽度（松手提交并持久化）；
 * 点一下（未拖动）则切换开合。拖动期间直接写两个布局壳的行内宽度，让浏览器
 * 在绘制时合并布局，避免每次指针移动都触发一次 React 提交。
 */
function SidebarRail({ className, ...props }: React.ComponentProps<"button">) {
  const { toggleSidebar } = useSidebar()
  const { commitWidth } = useSidebarResize()
  const { t } = useTranslation("ui")
  const toggleLabel = t(($) => $.toggle_sidebar)
  const didDragRef = React.useRef(false)
  const dragRef = React.useRef<{
    pointerId: number
    startX: number
    startWidth: number
    latestWidth: number
    direction: 1 | -1
    wrapperEl: HTMLElement
    gapEl: HTMLElement
    containerEl: HTMLElement
  } | null>(null)
  const cancelActiveDragRef = React.useRef<(() => void) | null>(null)

  React.useEffect(() => () => cancelActiveDragRef.current?.(), [])

  const onPointerDown = React.useCallback(
    (e: React.PointerEvent<HTMLButtonElement>) => {
      if (e.button !== 0 || e.isPrimary === false) return
      e.preventDefault()
      cancelActiveDragRef.current?.()
      didDragRef.current = false
      const railEl = e.currentTarget
      const sidebarEl = railEl.closest<HTMLElement>("[data-slot='sidebar']")
      const wrapperEl = railEl.closest<HTMLElement>("[data-slot='sidebar-wrapper']")
      const gapEl = sidebarEl?.querySelector<HTMLElement>("[data-slot='sidebar-gap']")
      const containerEl = sidebarEl?.querySelector<HTMLElement>("[data-slot='sidebar-container']")
      if (!sidebarEl || !wrapperEl || !gapEl || !containerEl) return

      const startWidth = clampSidebarWidth(containerEl.getBoundingClientRect().width)
      dragRef.current = {
        pointerId: e.pointerId,
        startX: e.clientX,
        startWidth,
        latestWidth: startWidth,
        direction: sidebarEl.dataset.side === "right" ? -1 : 1,
        wrapperEl,
        gapEl,
        containerEl,
      }

      wrapperEl.setAttribute("data-sidebar-resizing", "true")
      document.documentElement.setAttribute("data-sidebar-resizing", "true")

      let finished = false
      const finishDrag = (mode: "commit" | "cancel") => {
        if (finished) return
        finished = true

        document.removeEventListener("pointermove", onPointerMove)
        document.removeEventListener("pointerup", onPointerUp)
        document.removeEventListener("pointercancel", onPointerCancel)
        window.removeEventListener("blur", onWindowBlur)
        railEl.removeEventListener("lostpointercapture", onLostPointerCapture)

        const drag = dragRef.current
        if (drag) {
          if (mode === "commit" && didDragRef.current) {
            drag.wrapperEl.style.setProperty(
              "--sidebar-width",
              `${drag.latestWidth}px`
            )
            commitWidth(drag.latestWidth)
          }
          drag.gapEl.style.removeProperty("width")
          drag.containerEl.style.removeProperty("width")
          drag.wrapperEl.removeAttribute("data-sidebar-resizing")
        }

        dragRef.current = null
        cancelActiveDragRef.current = null
        document.documentElement.removeAttribute("data-sidebar-resizing")

        if (railEl.hasPointerCapture?.(e.pointerId)) {
          railEl.releasePointerCapture?.(e.pointerId)
        }
      }

      const onPointerMove = (event: PointerEvent) => {
        const drag = dragRef.current
        if (!drag || event.pointerId !== drag.pointerId) return

        const delta = (event.clientX - drag.startX) * drag.direction
        if (!didDragRef.current && Math.abs(delta) < SIDEBAR_DRAG_THRESHOLD) {
          return
        }

        didDragRef.current = true
        const nextWidth = clampSidebarWidth(drag.startWidth + delta)
        if (nextWidth === drag.latestWidth) return

        drag.latestWidth = nextWidth
        // 只有两个布局壳依赖实时宽度。直接写样式让浏览器在绘制时合并布局，
        // 不用走一次 React 提交、也不会让自定义属性失效波及整棵应用树。
        drag.gapEl.style.width = `${nextWidth}px`
        drag.containerEl.style.width = `${nextWidth}px`
      }
      const onPointerUp = (event: PointerEvent) => {
        if (event.pointerId === e.pointerId) finishDrag("commit")
      }
      const onPointerCancel = (event: PointerEvent) => {
        if (event.pointerId === e.pointerId) finishDrag("cancel")
      }
      const onLostPointerCapture = (event: PointerEvent) => {
        if (event.pointerId === e.pointerId) finishDrag("cancel")
      }
      const onWindowBlur = () => finishDrag("cancel")

      document.addEventListener("pointermove", onPointerMove)
      document.addEventListener("pointerup", onPointerUp)
      document.addEventListener("pointercancel", onPointerCancel)
      window.addEventListener("blur", onWindowBlur)
      railEl.addEventListener("lostpointercapture", onLostPointerCapture)
      cancelActiveDragRef.current = () => finishDrag("cancel")
      railEl.setPointerCapture?.(e.pointerId)
    },
    [commitWidth]
  )

  const handleClick = React.useCallback(() => {
    if (!didDragRef.current) toggleSidebar()
  }, [toggleSidebar])

  return (
    <button
      type="button"
      data-sidebar="rail"
      data-slot="sidebar-rail"
      aria-label={toggleLabel}
      tabIndex={-1}
      onClick={handleClick}
      onPointerDown={onPointerDown}
      title={toggleLabel}
      className={cn(
        "absolute inset-y-0 z-20 hidden w-4 touch-none cursor-ew-resize transition-[transform,background-color] ease-linear group-data-[side=left]:-right-4 group-data-[side=right]:left-0 after:absolute after:inset-y-0 after:start-1/2 after:w-[2px] hover:after:bg-sidebar-border sm:flex ltr:-translate-x-1/2 rtl:-translate-x-1/2",
        "group-data-[collapsible=offcanvas]:translate-x-0 group-data-[collapsible=offcanvas]:after:left-full hover:group-data-[collapsible=offcanvas]:bg-sidebar",
        "[[data-side=left][data-collapsible=offcanvas]_&]:-right-2",
        "[[data-side=right][data-collapsible=offcanvas]_&]:-left-2",
        className
      )}
      {...props}
    />
  )
}

/** SidebarInset：与 Sidebar 并排的主内容区（main）；variant=inset 时自动让出侧栏空隙并加卡片式外观 */
function SidebarInset({ className, ...props }: React.ComponentProps<"main">) {
  return (
    <main
      data-slot="sidebar-inset"
      className={cn(
        "relative flex w-full flex-1 flex-col bg-page-canvas lg:peer-data-[variant=inset]:m-2 lg:peer-data-[variant=inset]:ml-0 lg:peer-data-[variant=inset]:rounded-xl lg:peer-data-[variant=inset]:ring-1 lg:peer-data-[variant=inset]:ring-surface-border lg:peer-data-[variant=inset]:shadow-[var(--surface-shadow)] lg:peer-data-[variant=inset]:peer-data-[state=collapsed]:ml-2",
        className
      )}
      {...props}
    />
  )
}

/** SidebarInput：侧栏内的搜索/过滤输入框 */
function SidebarInput({
  className,
  ...props
}: React.ComponentProps<typeof Input>) {
  return (
    <Input
      data-slot="sidebar-input"
      data-sidebar="input"
      className={cn("h-8 w-full bg-background shadow-none", className)}
      {...props}
    />
  )
}

/** SidebarHeader：侧栏顶部区块 */
function SidebarHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-header"
      data-sidebar="header"
      className={cn("flex flex-col gap-2 p-2", className)}
      {...props}
    />
  )
}

/** SidebarFooter：侧栏底部区块（通常放用户菜单、设置入口） */
function SidebarFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-footer"
      data-sidebar="footer"
      className={cn("flex flex-col gap-2 p-2", className)}
      {...props}
    />
  )
}

/** SidebarSeparator：侧栏里的分隔线 */
function SidebarSeparator({
  className,
  ...props
}: React.ComponentProps<typeof Separator>) {
  return (
    <Separator
      data-slot="sidebar-separator"
      data-sidebar="separator"
      className={cn("mx-2 w-auto bg-sidebar-border", className)}
      {...props}
    />
  )
}

/** SidebarContent：侧栏中部可滚动的导航内容区（菜单都放这里） */
function SidebarContent({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-content"
      data-sidebar="content"
      className={cn(
        "no-scrollbar flex min-h-0 flex-1 flex-col gap-0 overflow-auto group-data-[collapsible=icon]:overflow-hidden",
        className
      )}
      {...props}
    />
  )
}

/** SidebarGroup：一组相关菜单的容器 */
function SidebarGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group"
      data-sidebar="group"
      className={cn("relative flex w-full min-w-0 flex-col p-2", className)}
      {...props}
    />
  )
}

/** SidebarGroupLabel：菜单组的标题；图标折叠态下会被隐藏 */
function SidebarGroupLabel({
  className,
  render,
  ...props
}: useRender.ComponentProps<"div"> & React.ComponentProps<"div">) {
  return useRender({
    defaultTagName: "div",
    props: mergeProps<"div">(
      {
        className: cn(
          "flex h-8 shrink-0 items-center rounded-md px-2 text-caption font-medium text-muted-foreground ring-sidebar-ring outline-hidden transition-[margin,opacity] duration-200 ease-linear group-data-[collapsible=icon]:-mt-8 group-data-[collapsible=icon]:opacity-0 focus-visible:ring-2 [&>svg]:size-4 [&>svg]:shrink-0",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-group-label",
      sidebar: "group-label",
    },
  })
}

/** SidebarGroupAction：钉在组标题右侧的操作按钮（如「新建」）；折叠态隐藏 */
function SidebarGroupAction({
  className,
  render,
  ...props
}: useRender.ComponentProps<"button"> & React.ComponentProps<"button">) {
  return useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(
          "absolute top-3.5 right-3 flex aspect-square w-5 items-center justify-center rounded-md p-0 text-sidebar-foreground ring-sidebar-ring outline-hidden transition-transform group-data-[collapsible=icon]:hidden after:absolute after:-inset-2 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 md:after:hidden [&>svg]:size-4 [&>svg]:shrink-0",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-group-action",
      sidebar: "group-action",
    },
  })
}

/** SidebarGroupContent：菜单组正文容器 */
function SidebarGroupContent({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-group-content"
      data-sidebar="group-content"
      className={cn("w-full text-body", className)}
      {...props}
    />
  )
}

/** SidebarMenu：菜单的 ul 容器 */
function SidebarMenu({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="sidebar-menu"
      data-sidebar="menu"
      className={cn("flex w-full min-w-0 flex-col gap-0", className)}
      {...props}
    />
  )
}

/** SidebarMenuItem：单个菜单项的 li 容器 */
function SidebarMenuItem({ className, ...props }: React.ComponentProps<"li">) {
  return (
    <li
      data-slot="sidebar-menu-item"
      data-sidebar="menu-item"
      className={cn("group/menu-item relative", className)}
      {...props}
    />
  )
}

/** 菜单按钮的视觉变体与尺寸（cva），折叠为图标条时自动方形成方形按钮 */
const sidebarMenuButtonVariants = cva(
  "peer/menu-button group/menu-button flex w-full items-center gap-2 overflow-hidden rounded-md p-2 text-left text-body ring-sidebar-ring outline-hidden transition-[width,height,padding] group-has-data-[sidebar=menu-action]/menu-item:pr-8 group-data-[collapsible=icon]:size-8! group-data-[collapsible=icon]:p-2! hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 active:bg-sidebar-accent active:text-sidebar-accent-foreground disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 data-open:hover:bg-sidebar-accent data-open:hover:text-sidebar-accent-foreground data-active:bg-sidebar-accent data-active:font-medium data-active:text-sidebar-accent-foreground data-active:hover:bg-sidebar-accent [&_svg]:size-4 [&_svg]:shrink-0 [&>span:last-child]:truncate",
  {
    variants: {
      variant: {
        default: "hover:bg-sidebar-accent hover:text-sidebar-accent-foreground",
        outline:
          "bg-background shadow-[0_0_0_1px_var(--color-sidebar-border)] hover:bg-sidebar-accent hover:text-sidebar-accent-foreground hover:shadow-[0_0_0_1px_var(--color-sidebar-accent)]",
      },
      size: {
        default: "h-8 text-body",
        sm: "h-7 text-caption",
        lg: "h-12 text-body group-data-[collapsible=icon]:p-0!",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

/**
 * SidebarMenuButton：菜单项按钮（导航链接通常用 render 换成 <a>/<AppLink>）。
 * isActive 表示当前项处于激活态；折叠为纯图标时若给了 tooltip，
 * 会包一层 Tooltip 在右侧显示说明。
 */
function SidebarMenuButton({
  render,
  isActive = false,
  variant = "default",
  size = "default",
  tooltip,
  className,
  ...props
}: useRender.ComponentProps<"button"> &
  React.ComponentProps<"button"> & {
    isActive?: boolean
    tooltip?: string | React.ComponentProps<typeof TooltipContent>
  } & VariantProps<typeof sidebarMenuButtonVariants>) {
  const { isCompact, state } = useSidebar()
  const comp = useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(sidebarMenuButtonVariants({ variant, size }), className),
      },
      props
    ),
    render: !tooltip ? render : <TooltipTrigger render={render} />,
    state: {
      slot: "sidebar-menu-button",
      sidebar: "menu-button",
      size,
      active: isActive,
    },
  })

  if (!tooltip) {
    return comp
  }

  if (typeof tooltip === "string") {
    tooltip = {
      children: tooltip,
    }
  }

  return (
    <Tooltip>
      {comp}
      <TooltipContent
        side="right"
        align="center"
        hidden={state !== "collapsed" || isCompact}
        {...tooltip}
      />
    </Tooltip>
  )
}

/** SidebarMenuAction：钉在菜单行右侧的操作按钮；showOnHover 时悬停该行才显示 */
function SidebarMenuAction({
  className,
  render,
  showOnHover = false,
  ...props
}: useRender.ComponentProps<"button"> &
  React.ComponentProps<"button"> & {
    showOnHover?: boolean
  }) {
  return useRender({
    defaultTagName: "button",
    props: mergeProps<"button">(
      {
        className: cn(
          "absolute top-1.5 right-1 flex aspect-square w-5 items-center justify-center rounded-md p-0 text-sidebar-foreground ring-sidebar-ring outline-hidden transition-transform group-data-[collapsible=icon]:hidden peer-hover/menu-button:text-sidebar-accent-foreground peer-data-[size=default]/menu-button:top-1.5 peer-data-[size=lg]/menu-button:top-2.5 peer-data-[size=sm]/menu-button:top-1 after:absolute after:-inset-2 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 md:after:hidden [&>svg]:size-4 [&>svg]:shrink-0",
          showOnHover &&
            "group-focus-within/menu-item:opacity-100 group-hover/menu-item:opacity-100 peer-data-active/menu-button:text-sidebar-accent-foreground aria-expanded:opacity-100 md:opacity-0",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-menu-action",
      sidebar: "menu-action",
    },
  })
}

/** SidebarMenuBadge：钉在菜单行右侧的角标（未读数等） */
function SidebarMenuBadge({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sidebar-menu-badge"
      data-sidebar="menu-badge"
      className={cn(
        "pointer-events-none absolute right-1 flex h-5 min-w-5 items-center justify-center rounded-md px-1 text-caption font-medium text-sidebar-foreground tabular-nums select-none group-data-[collapsible=icon]:hidden peer-hover/menu-button:text-sidebar-accent-foreground peer-data-[size=default]/menu-button:top-1.5 peer-data-[size=lg]/menu-button:top-2.5 peer-data-[size=sm]/menu-button:top-1 peer-data-active/menu-button:text-sidebar-accent-foreground",
        className
      )}
      {...props}
    />
  )
}

/** SidebarMenuSkeleton：菜单项加载占位（宽度随机 50%-90%）；showIcon 时前面加一个图标位 */
function SidebarMenuSkeleton({
  className,
  showIcon = false,
  ...props
}: React.ComponentProps<"div"> & {
  showIcon?: boolean
}) {
  // 文字条宽度在 50%–90% 之间随机
  const [width] = React.useState(() => {
    return `${Math.floor(Math.random() * 40) + 50}%`
  })

  return (
    <div
      data-slot="sidebar-menu-skeleton"
      data-sidebar="menu-skeleton"
      className={cn("flex h-8 items-center gap-2 rounded-md px-2", className)}
      {...props}
    >
      {showIcon && (
        <Skeleton
          className="size-4 rounded-md"
          data-sidebar="menu-skeleton-icon"
        />
      )}
      <Skeleton
        className="h-4 max-w-(--skeleton-width) flex-1"
        data-sidebar="menu-skeleton-text"
        style={
          {
            "--skeleton-width": width,
          } as React.CSSProperties
        }
      />
    </div>
  )
}

/** SidebarMenuSub：子菜单容器（缩进 + 左侧引导线）；折叠态隐藏 */
function SidebarMenuSub({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="sidebar-menu-sub"
      data-sidebar="menu-sub"
      className={cn(
        "mx-3.5 flex min-w-0 translate-x-px flex-col gap-1 border-l border-sidebar-border px-2.5 py-0.5 group-data-[collapsible=icon]:hidden",
        className
      )}
      {...props}
    />
  )
}

/** SidebarMenuSubItem：子菜单项的 li 容器 */
function SidebarMenuSubItem({
  className,
  ...props
}: React.ComponentProps<"li">) {
  return (
    <li
      data-slot="sidebar-menu-sub-item"
      data-sidebar="menu-sub-item"
      className={cn("group/menu-sub-item relative", className)}
      {...props}
    />
  )
}

/** SidebarMenuSubButton：子菜单项的链接/按钮 */
function SidebarMenuSubButton({
  render,
  size = "md",
  isActive = false,
  className,
  ...props
}: useRender.ComponentProps<"a"> &
  React.ComponentProps<"a"> & {
    size?: "sm" | "md"
    isActive?: boolean
  }) {
  return useRender({
    defaultTagName: "a",
    props: mergeProps<"a">(
      {
        className: cn(
          "flex h-7 min-w-0 -translate-x-px items-center gap-2 overflow-hidden rounded-md px-2 text-sidebar-foreground ring-sidebar-ring outline-hidden group-data-[collapsible=icon]:hidden hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 active:bg-sidebar-accent active:text-sidebar-accent-foreground disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 data-[size=md]:text-body data-[size=sm]:text-caption data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground data-active:hover:bg-sidebar-accent [&>span:last-child]:truncate [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-sidebar-accent-foreground",
          className
        ),
      },
      props
    ),
    render,
    state: {
      slot: "sidebar-menu-sub-button",
      sidebar: "menu-sub-button",
      size,
      active: isActive,
    },
  })
}

export {
  SIDEBAR_WRAPPER_FILL_CLASS,
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInput,
  SidebarInset,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarRail,
  SidebarSeparator,
  SidebarTrigger,
  useSidebar,
  useSidebarSafe,
}
