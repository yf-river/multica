// 通用头像裁剪器的画布渲染与编码逻辑。
//
// react-easy-crop 负责交互几何（平移/缩放/旋转），并通过 `onCropComplete`
// 以源图像素返回裁剪矩形。本模块把这个矩形转成固定尺寸的方形、压缩后的头像文件。

/** 编码后头像的边长（像素）。头像永远不需要原始位图大小。 */
export const AVATAR_OUTPUT_SIZE = 512;

const AVATAR_QUALITY = 0.85;

/** react-easy-crop 的 `croppedAreaPixels` 形状（源图像素坐标）。 */
export interface PixelCrop {
  x: number;
  y: number;
  width: number;
  height: number;
}

let webpEncodeSupport: boolean | null = null;

/**
 * 浏览器是否能通过 canvas *编码* WebP。Safari < 17 能解码 WebP 却不能编码，
 * toDataURL 会悄悄输出 PNG——所以我们探测 data URL 的 mime，而不是想当然。
 * @returns 是否支持 WebP 编码（结果会被缓存）。
 */
export function supportsWebpEncode(): boolean {
  if (webpEncodeSupport !== null) return webpEncodeSupport;
  try {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    webpEncodeSupport = canvas
      .toDataURL("image/webp")
      .startsWith("data:image/webp");
  } catch {
    webpEncodeSupport = false;
  }
  return webpEncodeSupport;
}

/**
 * 选择首选的输出格式：优先 WebP（保留透明通道、体积更小），不支持时回退 JPEG。
 * @returns 输出类型和对应的压缩质量。
 */
export function pickOutputType(): { type: string; quality: number } {
  return supportsWebpEncode()
    ? { type: "image/webp", quality: AVATAR_QUALITY }
    : { type: "image/jpeg", quality: AVATAR_QUALITY };
}

/**
 * 把编码后的 blob 包成 File，并把源文件扩展名替换为输出格式对应的扩展名。
 * @param blob 编码后的图片 blob。
 * @param sourceName 源文件名。
 * @param type 输出的 MIME 类型。
 * @returns 命名好的头像 File。
 */
export function blobToAvatarFile(
  blob: Blob,
  sourceName: string,
  type: string,
): File {
  const ext = type === "image/webp" ? "webp" : type === "image/png" ? "png" : "jpg";
  const base = sourceName.replace(/\.[^./\\]+$/, "") || "avatar";
  return new File([blob], `${base}.${ext}`, { type });
}

function canvasToBlob(
  canvas: HTMLCanvasElement,
  type: string,
  quality: number,
): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => (blob ? resolve(blob) : reject(new Error("Canvas is empty"))),
      type,
      quality,
    );
  });
}

function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.addEventListener("load", () => resolve(image));
    image.addEventListener("error", () => reject(new Error("Could not load image")));
    image.src = src;
  });
}

function toRadians(degrees: number): number {
  return (degrees * Math.PI) / 180;
}

/** 图像旋转后的外接矩形尺寸（像素）。 */
function rotatedBoundingBox(width: number, height: number, degrees: number) {
  const rad = toRadians(degrees);
  return {
    width: Math.abs(Math.cos(rad) * width) + Math.abs(Math.sin(rad) * height),
    height: Math.abs(Math.sin(rad) * width) + Math.abs(Math.cos(rad) * height),
  };
}

export interface RenderOptions {
  /** 输出的正方形边长（像素）。 */
  output: number;
  /** 输出 MIME 类型。 */
  type: string;
  /** 压缩质量 0–1。 */
  quality: number;
  /** 图像背后填充的背景色，用于不透明格式（如 JPEG）。 */
  background?: string;
}

/**
 * 把（可能旋转过的）裁剪区域画进正方形画布并编码。
 * `pixelCrop` 是 react-easy-crop 的 `croppedAreaPixels`，坐标基于旋转后图像的
 * 外接矩形空间——所以我们先把整张图旋转到外接矩形画布上，再从中取样裁剪区域。
 * @param imageSrc 源图片地址（object URL）。
 * @param pixelCrop 源图像素下的裁剪矩形。
 * @param rotation 旋转角度。
 * @param options 渲染选项（输出尺寸、类型、质量、背景色）。
 * @returns 编码后的图片 blob。
 */
export async function getCroppedAvatarBlob(
  imageSrc: string,
  pixelCrop: PixelCrop,
  rotation: number,
  options: RenderOptions,
): Promise<Blob> {
  const image = await loadImage(imageSrc);
  const bBox = rotatedBoundingBox(image.width, image.height, rotation);

  const rotated = document.createElement("canvas");
  rotated.width = Math.round(bBox.width);
  rotated.height = Math.round(bBox.height);
  const rctx = rotated.getContext("2d");
  if (!rctx) throw new Error("Canvas 2D context unavailable");
  rctx.translate(rotated.width / 2, rotated.height / 2);
  rctx.rotate(toRadians(rotation));
  rctx.drawImage(image, -image.width / 2, -image.height / 2);

  const out = document.createElement("canvas");
  out.width = options.output;
  out.height = options.output;
  const octx = out.getContext("2d");
  if (!octx) throw new Error("Canvas 2D context unavailable");
  octx.imageSmoothingQuality = "high";
  if (options.background) {
    octx.fillStyle = options.background;
    octx.fillRect(0, 0, options.output, options.output);
  }
  octx.drawImage(
    rotated,
    pixelCrop.x,
    pixelCrop.y,
    pixelCrop.width,
    pixelCrop.height,
    0,
    0,
    options.output,
    options.output,
  );
  return canvasToBlob(out, options.type, options.quality);
}
