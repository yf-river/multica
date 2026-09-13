/**
 * 供案例/文章 MDX 使用的「截图」组件：渲染带边框的图片与可选图注，
 * 复用在案例页与文档的截图展示。
 */
import Image from "next/image";

/** 截图组件的 props：图片地址、替代文本、宽高、可选图注与是否优先加载。 */
type ScreenshotProps = {
  src: string;
  alt: string;
  width: number;
  height: number;
  caption?: string;
  priority?: boolean;
};

/**
 * MDX 截图组件。
 *
 * @param props.src - 图片地址。
 * @param props.alt - 替代文本。
 * @param props.width / props.height - 图片原始尺寸（Next Image 要求）。
 * @param props.caption - 可选的图片下方说明文字。
 * @param props.priority - 是否 LCP 优先加载。
 * @returns 带边框的截图 figure。
 */
export function Screenshot({
  src,
  alt,
  width,
  height,
  caption,
  priority = false,
}: ScreenshotProps) {
  return (
    <figure className="my-10 -mx-4 sm:mx-0">
      <div className="overflow-hidden border border-[#0a0d12]/8 bg-[#f5f5f5]">
        <Image
          src={src}
          alt={alt}
          width={width}
          height={height}
          sizes="(max-width: 720px) 100vw, 720px"
          quality={85}
          priority={priority}
          className="block h-auto w-full"
        />
      </div>
      {caption ? (
        <figcaption className="mt-3 text-center text-label text-[#0a0d12]/45">
          {caption}
        </figcaption>
      ) : null}
    </figure>
  );
}
