/**
 * 共享缩放画布的平移/缩放数学——Mermaid 图与图片预览都经由本模块驱动
 * 各自的查看器。
 *
 * 内容从不需要自己缩放：Mermaid 图住在一个不能跑脚本的空沙箱 iframe 里，
 * `<img>` 也没有自带的缩放能力。所有交互都由宿主文档通过变换内容外层包裹
 * 元素来实现，所以本模块保持为纯 DOM-free 的数学：它是决定「内容在哪、
 * 有多大」的唯一地方。
 *
 * 坐标模型：内容包裹层 `transform-origin: 0 0`，且定位在视口左上角，
 * 于是变换按 `translate(x, y) scale(scale)` 施加。`x`/`y` 是内容左上角在
 * 视口坐标系里的像素；`scale` 是相对原始尺寸的倍数。
 */

/** 尺寸（宽高，px）。 */
export interface Size {
  width: number;
  height: number;
}

/** 二维点坐标（px）。 */
export interface Point {
  x: number;
  y: number;
}

/** 一次缩放变换：缩放倍数 + 内容左上角的平移量。 */
export interface ZoomTransform {
  scale: number;
  x: number;
  y: number;
}

// 默认的最小缩放下限。只是默认值：`computeMinScale` 会对无法在 25% 下
// 适配的内容继续调低它——见那边的注释。
export const MIN_SCALE = 0.25;
export const MAX_SCALE = 4;

// 内容必须有多少留在视口内。平移被这样钳制后，用户永远不可能把画布
// 甩到视野之外，然后面对一个除了 Reset 毫无办法的空视口。
const MIN_VISIBLE_PX = 48;

// 键盘/按钮的缩放步长。1.2 大约按 4 下翻一倍，手感跟手又不会跳过头
// 错过用户想要的缩放级别。
export const ZOOM_STEP = 1.2;

// 键盘平移步长：每次按方向键平移的视口像素数。
export const PAN_STEP_PX = 48;

/**
 * 把缩放倍数钳制到 [minScale, MAX_SCALE]。非法值回退到 1。
 */
export function clampScale(scale: number, minScale: number = MIN_SCALE): number {
  if (!Number.isFinite(scale)) return 1;
  return Math.min(MAX_SCALE, Math.max(minScale, scale));
}

/** 尺寸是否有效（有限且大于 0）。 */
function hasArea(size: Size): boolean {
  return (
    Number.isFinite(size.width) &&
    Number.isFinite(size.height) &&
    size.width > 0 &&
    size.height > 0
  );
}

/**
 * 让内容恰好适配视口的缩放倍数，且绝不放大超过原始尺寸。在很大的查看器
 * 里打开小图/缩略图，应显示在 100%，而不是被放得看起来坏了——与 macOS
 * 预览同一约定。
 *
 * 刻意不用 MIN_SCALE 兜底：一张比画布高十倍的整页截图约在 0.1 就能适配，
 * 若这里返回 0.25，一打开它就被裁掉了。
 */
export function computeFitScale(content: Size, viewport: Size): number {
  if (!hasArea(content) || !hasArea(viewport)) return 1;

  return Math.min(
    MAX_SCALE,
    Math.min(1, viewport.width / content.width, viewport.height / content.height),
  );
}

/**
 * 给定内容/视口对的缩放下限：通常是 MIN_SCALE，但绝不会高于适配缩放。
 *
 * 没有它，「适配视图」对在任一方向上超过画布 4 倍的内容（正是长截图那种
 * 情况）永远无法到达——因为每次钳制都会把缩放弹回 25%，内容被裁掉，
 * 永远没法看全。
 */
export function computeMinScale(content: Size, viewport: Size): number {
  return Math.min(MIN_SCALE, computeFitScale(content, viewport));
}

/** 在 `viewport` 内以 `scale` 倍数居中摆放 `content`。 */
export function centerTransform(
  content: Size,
  viewport: Size,
  scale: number,
): ZoomTransform {
  return {
    scale,
    x: (viewport.width - content.width * scale) / 2,
    y: (viewport.height - content.height * scale) / 2,
  };
}

/** 默认视图：适配视口并居中。 */
export function computeFitTransform(content: Size, viewport: Size): ZoomTransform {
  return centerTransform(content, viewport, computeFitScale(content, viewport));
}

/**
 * 保证内容至少 `MIN_VISIBLE_PX` 留在视口内（内容本身比这个边距还小时
 * 则全部保留），这样画布永远不可能被平移到什么都没有。
 */
export function clampTransform(
  transform: ZoomTransform,
  content: Size,
  viewport: Size,
): ZoomTransform {
  if (!hasArea(content) || !hasArea(viewport)) return transform;

  const scale = clampScale(transform.scale, computeMinScale(content, viewport));
  const scaledWidth = content.width * scale;
  const scaledHeight = content.height * scale;
  const marginX = Math.min(MIN_VISIBLE_PX, scaledWidth);
  const marginY = Math.min(MIN_VISIBLE_PX, scaledHeight);

  return {
    scale,
    x: Math.min(
      Math.max(transform.x, marginX - scaledWidth),
      viewport.width - marginX,
    ),
    y: Math.min(
      Math.max(transform.y, marginY - scaledHeight),
      viewport.height - marginY,
    ),
  };
}

/**
 * 缩放到 `nextScale`，同时把 `anchor`（视口坐标）下的内容点钉在屏幕原位。
 * 这正是滚轮/捏合缩放能跟随光标/手指、而不是漂向某个角的原因。
 */
export function zoomToAt(
  transform: ZoomTransform,
  nextScale: number,
  anchor: Point,
  content: Size,
  viewport: Size,
): ZoomTransform {
  const scale = clampScale(nextScale, computeMinScale(content, viewport));
  if (scale === transform.scale) return transform;

  const ratio = scale / transform.scale;

  return clampTransform(
    {
      scale,
      x: anchor.x - (anchor.x - transform.x) * ratio,
      y: anchor.y - (anchor.y - transform.y) * ratio,
    },
    content,
    viewport,
  );
}

/** 以 `anchor` 为锚点做乘法缩放（滚轮一格、+/- 键、工具栏按钮）。 */
export function zoomByAt(
  transform: ZoomTransform,
  factor: number,
  anchor: Point,
  content: Size,
  viewport: Size,
): ZoomTransform {
  return zoomToAt(transform, transform.scale * factor, anchor, content, viewport);
}

/** 绕视口中心缩放——键盘/按钮应使用的锚点。 */
export function zoomByAtCenter(
  transform: ZoomTransform,
  factor: number,
  content: Size,
  viewport: Size,
): ZoomTransform {
  return zoomByAt(
    transform,
    factor,
    { x: viewport.width / 2, y: viewport.height / 2 },
    content,
    viewport,
  );
}

/**
 * 在当前缩放下按给定增量平移（已做视野钳制）。
 */
export function panBy(
  transform: ZoomTransform,
  deltaX: number,
  deltaY: number,
  content: Size,
  viewport: Size,
): ZoomTransform {
  return clampTransform(
    { scale: transform.scale, x: transform.x + deltaX, y: transform.y + deltaY },
    content,
    viewport,
  );
}

/** 两个活跃触点间的距离——捏合手势的缩放信号。 */
export function distanceBetween(a: Point, b: Point): number {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

/** 两点的中点——捏合缩放围绕的锚点。 */
export function midpointOf(a: Point, b: Point): Point {
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
}

/**
 * 把一格滚轮滚动换算成缩放因子。`deltaMode` 很关键：鼠标按行（1）或
 * 整页（2）报，触控板按像素（0）报——若直接拿裸 `deltaY` 做比较，
 * 两种设备里必有一种完全没法用。
 */
export function wheelZoomFactor(deltaY: number, deltaMode: number): number {
  const pixels = deltaMode === 1 ? deltaY * 16 : deltaMode === 2 ? deltaY * 100 : deltaY;
  // 指数映射保证缩放对称：等量反向滚动能回到原缩放。除数 400 是触控板
  // 灵敏度调校值。
  return Math.exp(-pixels / 400);
}
