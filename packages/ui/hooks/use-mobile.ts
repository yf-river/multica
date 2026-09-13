import * as React from "react"

// 本文件提供「窗口宽度是否低于某断点」的响应式判断 hooks，
// 用于驱动移动/紧凑两种界面形态的切换。

/** 移动端断点：768px 以下视为手机 */
const MOBILE_BREAKPOINT = 768
// Tailwind 的 `lg`。低于它时，256px 导航 + 320px 列表栏之后，「列表/详情」
// 的阅读宽度只剩两三百像素，所以这类双栏会折叠成手机上那种单栏。
const COMPACT_BREAKPOINT = 1024

/**
 * 判断窗口宽度是否低于给定断点。SSR 友好：首屏先返回 false，
 * 挂载后立刻同步一次真实值，之后随窗口 resize 自动更新。
 */
function useIsBelow(breakpoint: number) {
  const [isBelow, setIsBelow] = React.useState<boolean | undefined>(undefined)

  React.useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${breakpoint - 1}px)`)
    const onChange = () => {
      setIsBelow(window.innerWidth < breakpoint)
    }
    mql.addEventListener("change", onChange)
    setIsBelow(window.innerWidth < breakpoint)
    return () => mql.removeEventListener("change", onChange)
  }, [breakpoint])

  return !!isBelow
}

/** 是否处于移动端形态（< 768px） */
export function useIsMobile() {
  return useIsBelow(MOBILE_BREAKPOINT)
}

/** 是否处于紧凑形态（< 1024px，lg 以下）：侧栏会退化为抽屉、双栏会折叠 */
export function useIsCompact() {
  return useIsBelow(COMPACT_BREAKPOINT)
}
