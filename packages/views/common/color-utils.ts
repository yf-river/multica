// 通用颜色选择器背后的颜色数学（见 color-picker.tsx）。
//
// 值以 `#rrggbb` hex 字符串传输（API 存储的格式）；选择器内部用 HSV 编辑，
// 因为饱和度/明度正好对应它的二维区域。

/** HSV 颜色模型，色相 0–360、饱和度与明度均为 0–1。 */
export interface Hsv {
  /** 色相，0–360 度。 */
  h: number;
  /** 饱和度 0–1。 */
  s: number;
  /** 明度（亮度）0–1。 */
  v: number;
}

/**
 * 规范化颜色输入：统一成小写 `#rrggbb`；当不是 6 位 hex 颜色时返回 null。
 * @param input 用户输入的颜色字符串。
 * @returns 规范化后的 hex 或 null。
 */
export function normalizeHex(input: string): string | null {
  const match = /^#?([0-9a-f]{6})$/i.exec(input.trim());
  return match ? `#${match[1]?.toLowerCase()}` : null;
}

/**
 * 把 hex 颜色转成 HSV。
 * @param hex hex 颜色字符串。
 * @returns HSV 对象，非法输入返回 null。
 */
export function hexToHsv(hex: string): Hsv | null {
  const normalized = normalizeHex(hex);
  if (!normalized) return null;
  const r = parseInt(normalized.slice(1, 3), 16) / 255;
  const g = parseInt(normalized.slice(3, 5), 16) / 255;
  const b = parseInt(normalized.slice(5, 7), 16) / 255;
  const max = Math.max(r, g, b);
  const delta = max - Math.min(r, g, b);
  let h = 0;
  if (delta !== 0) {
    if (max === r) h = ((g - b) / delta) % 6;
    else if (max === g) h = (b - r) / delta + 2;
    else h = (r - g) / delta + 4;
    h = (h * 60 + 360) % 360;
  }
  return { h, s: max === 0 ? 0 : delta / max, v: max };
}

/**
 * 把 HSV 颜色转成 `#rrggbb` hex 字符串。
 * @param param0 HSV 颜色（h/s/v）。
 * @returns hex 字符串。
 */
export function hsvToHex({ h, s, v }: Hsv): string {
  const channel = (n: number): string => {
    const k = (n + h / 60) % 6;
    const value = v - v * s * Math.max(0, Math.min(k, 4 - k, 1));
    return Math.round(value * 255)
      .toString(16)
      .padStart(2, "0");
  };
  return `#${channel(5)}${channel(3)}${channel(1)}`;
}

// 随机颜色在 HSL 空间采样，并限制在预设调色板所在的鲜亮色带内，这样随机结果
// 永远不会落到近白、近黑或褪色灰上。当把当前颜色作为 `avoid` 传入时，新色相会
// 与它保持最小圆周距离，让每次点击都产生明显不同的颜色。

const MIN_HUE_DISTANCE = 40;
const SATURATION_RANGE: readonly [number, number] = [0.62, 0.9];
const LIGHTNESS_RANGE: readonly [number, number] = [0.46, 0.62];

function randomIn([min, max]: readonly [number, number]): number {
  return min + Math.random() * (max - min);
}

function hslToHex(hue: number, saturation: number, lightness: number): string {
  const channel = (n: number): string => {
    const k = (n + hue / 30) % 12;
    const a = saturation * Math.min(lightness, 1 - lightness);
    const value = lightness - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
    return Math.round(value * 255)
      .toString(16)
      .padStart(2, "0");
  };
  return `#${channel(0)}${channel(8)}${channel(4)}`;
}

/**
 * 返回一个随机的鲜亮颜色（`#rrggbb`）。通过 `avoid` 传入当前颜色，
 * 可保证结果看起来是另一个不同的颜色。
 * @param avoid 要避开的当前颜色（可选）。
 * @returns 随机生成的 hex 颜色。
 */
export function randomOptionColor(avoid?: string): string {
  const avoidHsv = avoid ? hexToHsv(avoid) : null;
  const avoidHue =
    avoidHsv && avoidHsv.s > 0 && avoidHsv.v > 0 ? avoidHsv.h : null;
  const hue =
    avoidHue === null
      ? Math.random() * 360
      : (avoidHue +
          MIN_HUE_DISTANCE +
          Math.random() * (360 - MIN_HUE_DISTANCE * 2)) %
        360;
  return hslToHex(hue, randomIn(SATURATION_RANGE), randomIn(LIGHTNESS_RANGE));
}
