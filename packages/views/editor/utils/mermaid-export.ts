/**
 * Mermaid 图的导出辅助（`.mmd` 源码、`.svg`、`.png`）。
 *
 * 导出在宿主文档里针对 Mermaid 返回的 SVG 标记进行——绝不伸手进预览
 * iframe（它是 `sandbox=""`，本来就刻意不可达）。因此标记必须先做成
 * 独立可用的：Mermaid 是为嵌入而渲染的，会输出透明背景、按宿主容器
 * 调好的 `max-width` 样式、以及 `font-family: inherit`，这些在单独存成
 * 文件后全都不能成立。
 */

const SVG_NAMESPACE = "http://www.w3.org/2000/svg";

// 按高于 CSS 像素的分辨率栅格化，PNG 在放大或粘进文档时才不至于糊。
// 2 倍对应典型 HiDPI 屏幕，又不会产出巨大的文件。
const PNG_PIXEL_RATIO = 2;

export interface ExportSvgOptions {
  /** 不透明页面底色，避免导出的文件在透明背景上再叠一层透明。 */
  background: string;
  /** 具体字体栈，替换 Mermaid 的 `inherit`——后者在文件里没有可继承的来源。 */
  fontFamily: string;
  width: number;
  height: number;
}

interface ViewBox {
  minX: number;
  minY: number;
  width: number;
  height: number;
}

function readViewBox(root: SVGElement, fallback: Size): ViewBox {
  const parts = (root.getAttribute("viewBox") ?? "")
    .split(/[\s,]+/)
    .map((value) => Number.parseFloat(value));

  if (parts.length === 4 && parts.every((value) => Number.isFinite(value))) {
    const [minX, minY, width, height] = parts as [number, number, number, number];
    if (width > 0 && height > 0) return { minX, minY, width, height };
  }

  return { minX: 0, minY: 0, width: fallback.width, height: fallback.height };
}

interface Size {
  width: number;
  height: number;
}

/**
 * 重写 `style` 属性里的各条声明。
 *
 * 在属性字符串层面做，而不是用 `.style` CSSStyleDeclaration API：从 XML
 * 文档解析出的 SVG 根节点不一定暴露该 API（jsdom 就完全不实现），在真实
 * 浏览器之外碰 `.style` 会直接抛错。
 */
function mergeStyleAttribute(
  existing: string | null,
  overrides: Record<string, string>,
  remove: string[],
): string {
  const declarations = new Map<string, string>();

  for (const part of (existing ?? "").split(";")) {
    const separator = part.indexOf(":");
    if (separator === -1) continue;
    const property = part.slice(0, separator).trim().toLowerCase();
    if (property) declarations.set(property, part.slice(separator + 1).trim());
  }

  for (const property of remove) declarations.delete(property);
  for (const [property, value] of Object.entries(overrides)) {
    declarations.set(property, value);
  }

  return [...declarations]
    .map(([property, value]) => `${property}: ${value}`)
    .join("; ");
}

/**
 * 把 Mermaid 产出的内嵌 SVG 标记重写成一份可独立打开的文档。
 *
 * @returns 序列化后的 SVG；标记无法解析时返回 null。
 */
export function buildExportSvg(
  svgMarkup: string,
  { background, fontFamily, width, height }: ExportSvgOptions,
): string | null {
  const parsed = new DOMParser().parseFromString(svgMarkup, "image/svg+xml");
  if (parsed.querySelector("parsererror")) return null;

  const root = parsed.documentElement as unknown as SVGElement;
  if (root.nodeName.toLowerCase() !== "svg") return null;

  root.setAttribute("xmlns", SVG_NAMESPACE);
  // 写死像素尺寸：`width="100%"`（Mermaid 默认）在独立文件里没有可参照的
  // 百分比基准，而且走 PNG 路径的 <img> 加载它时也需要固有尺寸才能绘制。
  root.setAttribute("width", String(width));
  root.setAttribute("height", String(height));
  root.setAttribute(
    "style",
    mergeStyleAttribute(
      root.getAttribute("style"),
      { "background-color": background, "font-family": fontFamily },
      // Mermaid 会把内嵌 SVG 调成宿主容器的宽度；保留它会让导出被裁剪成
      // 页面当时恰好有的宽度。
      ["max-width"],
    ),
  );

  const viewBox = readViewBox(root, { width, height });
  const backgroundRect = parsed.createElementNS(SVG_NAMESPACE, "rect");
  // 覆盖整个 viewBox 而不是 0,0 到 100%,100%：Mermaid 对带留白的图会输出
  // 负的 viewBox 原点，锚在 0,0 的矩形会把这些边距留成透明。
  backgroundRect.setAttribute("x", String(viewBox.minX));
  backgroundRect.setAttribute("y", String(viewBox.minY));
  backgroundRect.setAttribute("width", String(viewBox.width));
  backgroundRect.setAttribute("height", String(viewBox.height));
  backgroundRect.setAttribute("fill", background);
  root.insertBefore(backgroundRect, root.firstChild);

  return new XMLSerializer().serializeToString(root);
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error("Failed to load SVG for PNG export"));
    image.src = url;
  });
}

/**
 * 把独立的 SVG 标记栅格化成 PNG blob。
 *
 * blob URL 同源，且标记里没有任何外部引用（字体已被 buildExportSvg
 * 换成具体字体栈），所以 canvas 不会被污染，`toBlob` 可以正常读回像素。
 */
export async function renderSvgToPngBlob(
  standaloneSvg: string,
  { width, height }: Size,
  pixelRatio: number = PNG_PIXEL_RATIO,
): Promise<Blob> {
  const svgBlob = new Blob([standaloneSvg], { type: "image/svg+xml;charset=utf-8" });
  const url = URL.createObjectURL(svgBlob);

  try {
    const image = await loadImage(url);
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(width * pixelRatio));
    canvas.height = Math.max(1, Math.round(height * pixelRatio));

    const context = canvas.getContext("2d");
    if (!context) throw new Error("Canvas 2D context unavailable for PNG export");
    context.drawImage(image, 0, 0, canvas.width, canvas.height);

    const blob = await new Promise<Blob | null>((resolve) =>
      canvas.toBlob(resolve, "image/png"),
    );
    if (!blob) throw new Error("Failed to encode PNG");

    return blob;
  } finally {
    URL.revokeObjectURL(url);
  }
}

/**
 * 触发浏览器下载一个 blob。
 */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  // 放到下一个任务再 revoke：Safari 在点击还在同步处理时若 URL 失效会
  // 中止下载。
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/**
 * 从图的第一个有意义行推导文件名主干，让一批导出的文件可以扫视识别，
 * 而不是清一色的 `diagram (3).png`。
 */
export function diagramFilenameStem(chart: string): string {
  const firstLine = chart
    .split("\n")
    .map((line) => line.trim())
    .find((line) => line.length > 0 && !line.startsWith("%%"));

  const slug = (firstLine ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");

  return slug || "diagram";
}
