"use client"

/**
 * 装饰性的「点阵球体」动画背景，移植自 ReUI 的 onboarding-3 区块。
 *
 * 纯展示组件：只在 canvas 上画点，不读任何产品状态，所以放在原子组件层。
 * 尊重 `prefers-reduced-motion`——用户要求减少动态时只静态画一帧，不动画。
 */

import { useEffect, useRef } from "react"

import { cn } from "@multica/ui/lib/utils"

/** DotSphere 组件的 props 定义 */
interface DotSphereProps {
  /** 外层 class 名 */
  className?: string
  /** 点阵里相邻两个点的间距（像素） */
  dotGap?: number
  /** 动画形态：'travel' 球沿斜线往返穿梭 / 'wave' 球在原地做脉动扩散 */
  motion?: "travel" | "wave"
  /** 球（发光中心）的数量 */
  sphereCount?: number
  /** 球的半径：像素数值，或相对画布较长边的一个百分比字符串 */
  sphereRadius?: number | `${number}%`
  /** 单个点能画到的最大半径（像素），离球心越近点越大 */
  dotRadiusMax?: number
  /** 动画速度系数 */
  speed?: number
  /** 画布背景颜色 */
  bgColor?: string
  /** 点的颜色 */
  dotColor?: string
  /** 是否让第一个球跟随鼠标移动 */
  followMouse?: boolean
}

/** 一条运动路径：球心从 from 点（相对坐标 0..1）匀速移动到 to 点 */
interface SpherePath {
  from: Point
  to: Point
}

/** 一个球的基础档案：错开的起始相位、半径/点大小的缩放、速度系数 */
interface SphereProfile {
  phase: number
  radius: number
  dot: number
  speed: number
}

/** 二维坐标点 */
interface Point {
  x: number
  y: number
}

/** 单个球在运行期保存的全部可变状态 */
interface DotSphereState {
  /** 当前球心（像素坐标） */
  center: Point
  /** wave 模式下的固定锚点（相对坐标 0..1） */
  anchor: Point
  /** travel 模式的起点（相对坐标） */
  from: Point
  /** travel 模式的终点（相对坐标） */
  to: Point
  /** 当前进度 0..1（travel 一程 / wave 一个脉动周期） */
  progress: number
  /** 球半径（像素） */
  radius: number
  /** 半径相对基准的缩放 */
  radiusScale: number
  /** 单点最大半径（像素） */
  dotRadiusMax: number
  /** 点大小相对基准的缩放 */
  dotScale: number
  /** 该球的速度系数 */
  speed: number
  /** 点的颜色 */
  color: string
  /** 球整体的不透明度 */
  opacity: number
}

/** 默认的点颜色：半透明的靛紫色 */
const DOT_SPHERE_COLOR = "rgba(129, 140, 248, 0.5)"

/** travel 模式为各球准备的斜向运动路径（坐标是相对画布宽高的 0..1） */
const SPHERE_PATHS: readonly [SpherePath, ...SpherePath[]] = [
  {
    from: { x: -0.28, y: 0.18 },
    to: { x: 1.28, y: 0.78 },
  },
  {
    from: { x: 1.28, y: 0.82 },
    to: { x: -0.28, y: 0.22 },
  },
  {
    from: { x: 0.18, y: 1.24 },
    to: { x: 0.82, y: -0.24 },
  },
]

/** wave 模式各球的固定锚点位置（相对坐标 0..1，形成大致对称的分布） */
const WAVE_SPHERE_ANCHORS: readonly [Point, ...Point[]] = [
  { x: 0.28, y: 0.08 },
  { x: 0.76, y: 0.28 },
  { x: 0.38, y: 0.5 },
  { x: 0.78, y: 0.72 },
  { x: 0.34, y: 0.93 },
]

/** 各球的基础档案（相位错开，让运动不齐步） */
const SPHERE_PROFILES: readonly [SphereProfile, ...SphereProfile[]] = [
  { phase: 0, radius: 1, dot: 1, speed: 1 },
  { phase: 0.2, radius: 0.94, dot: 0.96, speed: 1 },
  { phase: 0.4, radius: 1, dot: 1, speed: 1 },
  { phase: 0.6, radius: 0.94, dot: 0.96, speed: 1 },
  { phase: 0.8, radius: 0.9, dot: 0.94, speed: 1 },
]

/** 把数值夹在 [min, max] 区间内 */
function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max)
}

/** 在 start..end 之间按 amount（0..1）线性插值 */
function lerp(start: number, end: number, amount: number) {
  return start + (end - start) * amount
}

/** 正弦缓动：让往返运动看起来更自然（0.5 - cos(πx)/2） */
function easeInOutSine(value: number) {
  return 0.5 - Math.cos(value * Math.PI) * 0.5
}

/** smoothstep 平滑函数：在 edge0..edge1 之间做 S 形过渡 */
function smoothstep(edge0: number, edge1: number, value: number) {
  const t = clamp((value - edge0) / (edge1 - edge0), 0, 1)

  return t * t * (3 - 2 * t)
}

/** travel 模式随进度变化的透明度：两头淡入淡出，中间全程可见 */
function getTravelOpacity(progress: number) {
  const fadeIn = smoothstep(0.03, 0.24, progress)
  const fadeOut = 1 - smoothstep(0.76, 0.97, progress)

  return Math.min(fadeIn, fadeOut)
}

/** wave 模式随进度变化的透明度：按周期脉动（更亮暗交替的光晕感） */
function getWaveOpacity(progress: number) {
  const pulse = (1 - Math.cos(progress * Math.PI * 2)) * 0.5

  return 0.16 + Math.pow(pulse, 1.28) * 0.84
}

/**
 * 把 sphereRadius 解析成像素值：数字直接用；百分比字符串按画布较长边换算；
 * 解析失败时回退成较长边本身。
 */
function resolveSphereRadius(
  radius: NonNullable<DotSphereProps["sphereRadius"]>,
  windowSize: { w: number; h: number }
) {
  if (typeof radius === "number") {
    return radius
  }

  const percent = Number.parseFloat(radius)

  if (Number.isNaN(percent)) {
    return Math.max(windowSize.w, windowSize.h)
  }

  return Math.max(windowSize.w, windowSize.h) * (percent / 100)
}

/**
 * DotSphere：全屏装饰用「发光点阵」动画。
 * 整个画面铺满小圆点，靠近某个球心（圆形区域）的点按距离变亮变大，
 * 视觉上就像有个发光球在点阵里移动。用 canvas 逐帧绘制。
 *
 * @param props 见 DotSphereProps 定义
 * @returns 铺满容器的 canvas 元素
 */
export function DotSphere({
  className,
  dotGap = 20,
  motion = "travel",
  sphereCount = 3,
  sphereRadius = 200,
  dotRadiusMax = 3,
  speed = 0.18,
  bgColor = "oklch(0.145 0 0)",
  dotColor = DOT_SPHERE_COLOR,
  followMouse = false,
}: DotSphereProps) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const stateRef = useRef({
    windowSize: { w: 0, h: 0 },
    circleNumber: { x: 0, y: 0 },
    posStart: { x: 0, y: 0 },
    spheres: [] as DotSphereState[],
    animationId: 0,
  })

  useEffect(() => {
    const canvas = canvasRef.current

    if (!canvas) {
      return
    }

    const ctx = canvas.getContext("2d")

    if (!ctx) {
      return
    }

    const canvasElement = canvas
    const context = ctx
    const state = stateRef.current
    const prefersReducedMotion = window.matchMedia(
      "(prefers-reduced-motion: reduce)"
    ).matches

    state.spheres = createSpheres()

    function createSpheres() {
      const count = Math.max(1, sphereCount)

      return Array.from({ length: count }, (_, index) => {
        // `index % length` 恒在数组范围内，但 TS 证明不了这点。
        // 靠上面的元组类型保证下标 0 永远合法，作为兜底而不是用 `!` 强断。
        const path = SPHERE_PATHS[index % SPHERE_PATHS.length] ?? SPHERE_PATHS[0]
        const anchor =
          WAVE_SPHERE_ANCHORS[index % WAVE_SPHERE_ANCHORS.length] ??
          WAVE_SPHERE_ANCHORS[0]
        const profile =
          SPHERE_PROFILES[index % SPHERE_PROFILES.length] ?? SPHERE_PROFILES[0]

        return {
          center: { x: 0, y: 0 },
          anchor,
          from: path.from,
          to: path.to,
          progress: profile.phase,
          radius: 0,
          radiusScale: profile.radius,
          dotRadiusMax: 0,
          dotScale: profile.dot,
          speed: speed * profile.speed,
          color: dotColor,
          opacity: 0,
        }
      })
    }

    function setDotParams() {
      state.circleNumber = {
        x: Math.floor(state.windowSize.w / dotGap) + 2,
        y: Math.floor(state.windowSize.h / dotGap) + 1,
      }

      state.posStart = {
        x: Math.round(
          (state.windowSize.w - (state.circleNumber.x - 1) * dotGap) / 2
        ),
        y: Math.round(
          (state.windowSize.h - (state.circleNumber.y - 1) * dotGap) / 2
        ),
      }
    }

    function updateSpherePosition(sphere: DotSphereState) {
      if (motion === "wave") {
        sphere.center.x = sphere.anchor.x * state.windowSize.w
        sphere.center.y = sphere.anchor.y * state.windowSize.h
        sphere.opacity = getWaveOpacity(sphere.progress)
        return
      }

      const easedProgress = easeInOutSine(sphere.progress)

      sphere.center.x =
        lerp(sphere.from.x, sphere.to.x, easedProgress) * state.windowSize.w
      sphere.center.y =
        lerp(sphere.from.y, sphere.to.y, easedProgress) * state.windowSize.h
      sphere.opacity = getTravelOpacity(sphere.progress)
    }

    function handleResize() {
      const bounds = canvasElement.getBoundingClientRect()
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      const width = Math.max(1, Math.floor(bounds.width))
      const height = Math.max(1, Math.floor(bounds.height))

      state.windowSize = { w: width, h: height }
      canvasElement.width = Math.floor(width * dpr)
      canvasElement.height = Math.floor(height * dpr)
      context.setTransform(dpr, 0, 0, dpr, 0, 0)

      setDotParams()
      state.spheres.forEach((sphere) => {
        sphere.radius =
          resolveSphereRadius(sphereRadius, state.windowSize) *
          sphere.radiusScale
        sphere.dotRadiusMax = dotRadiusMax * sphere.dotScale
        updateSpherePosition(sphere)
      })
    }

    function getDistance(
      x: number,
      y: number,
      center: { x: number; y: number }
    ) {
      const distanceX = x - center.x
      const distanceY = y - center.y

      return Math.sqrt(distanceX * distanceX + distanceY * distanceY)
    }

    function getAlpha(distance: number, radius: number) {
      return 1 - distance / radius
    }

    function getRadius(alpha: number, radiusMax: number) {
      return radiusMax * alpha
    }

    function drawDots() {
      context.beginPath()
      context.fillStyle = bgColor
      context.rect(0, 0, state.windowSize.w, state.windowSize.h)
      context.fill()
      context.closePath()
      context.globalCompositeOperation = "lighter"

      state.spheres.forEach((sphere) => {
        if (sphere.opacity <= 0.01) {
          return
        }

        for (let i = 0; i < state.circleNumber.x; i++) {
          for (let j = 0; j < state.circleNumber.y; j++) {
            const gapX = j % 2 === 0 ? -dotGap / 2 : 0
            const x = state.posStart.x + gapX + i * dotGap
            const y = state.posStart.y + j * dotGap
            const distance = getDistance(x, y, sphere.center)

            if (distance <= sphere.radius) {
              const alpha = getAlpha(distance, sphere.radius)
              const radius = getRadius(alpha, sphere.dotRadiusMax)

              context.save()
              context.globalAlpha = alpha * sphere.opacity
              context.beginPath()
              context.fillStyle = sphere.color
              context.arc(x, y, radius, 0, 2 * Math.PI, false)
              context.fill()
              context.closePath()
              context.restore()
            }
          }
        }
      })

      context.globalCompositeOperation = "source-over"
    }

    function moveSpheres(event: MouseEvent | null) {
      const bounds = event ? canvasElement.getBoundingClientRect() : null

      state.spheres.forEach((sphere, index) => {
        if (event && followMouse && index === 0 && bounds) {
          sphere.center.x = event.clientX - bounds.left
          sphere.center.y = event.clientY - bounds.top
          sphere.opacity = 1
          return
        }

        if (followMouse && index === 0) {
          return
        }

        sphere.progress =
          (sphere.progress +
            sphere.speed * (motion === "wave" ? 0.0021 : 0.0035)) %
          1
        updateSpherePosition(sphere)
      })
    }

    function render() {
      if (!prefersReducedMotion) {
        moveSpheres(null)
      }

      drawDots()
    }

    function draw() {
      state.animationId = window.requestAnimationFrame(draw)
      render()
    }

    function handleMouseMove(event: MouseEvent) {
      if (followMouse) {
        moveSpheres(event)
      }
    }

    const resizeObserver = new ResizeObserver(handleResize)

    resizeObserver.observe(canvasElement)
    handleResize()
    render()

    if (!prefersReducedMotion) {
      draw()
    }

    if (followMouse) {
      window.addEventListener("mousemove", handleMouseMove)
    }

    return () => {
      resizeObserver.disconnect()
      window.removeEventListener("mousemove", handleMouseMove)
      window.cancelAnimationFrame(state.animationId)
    }
  }, [
    dotGap,
    motion,
    sphereCount,
    sphereRadius,
    dotRadiusMax,
    speed,
    bgColor,
    dotColor,
    followMouse,
  ])

  return (
    <canvas
      ref={canvasRef}
      data-slot="dot-sphere"
      className={cn("absolute inset-0 h-full w-full", className)}
    />
  )
}