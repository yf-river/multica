"use client";

/**
 * AttachmentPreviewModal — 附件的全屏内联预览。
 *
 * 所有可预览类型共用一个弹窗，处理 7 种 PreviewKind：
 *
 *   - image : 在共享 ZoomCanvas 上渲染 <img> —— 打开时先适配，之后支持滚轮 /
 *             拖拽 / 捏合 / 双击 / 键盘缩放，控制方式和 Mermaid 查看器一致。
 *             它取代了之前独立的 ImageLightbox。
 *   - pdf   : <iframe src={download_url}> —— 依赖浏览器的 PDF 查看器。
 *   - video : <video controls src={download_url}>
 *   - audio : <audio controls src={download_url}>
 *
 *   - markdown : 通过 api.getAttachmentTextContent 抓取文本，再用现成的
 *                ReadonlyContent 渲染（包含完整的 mention/mermaid/katex 管线）。
 *   - html     : 抓取文本后交给 <iframe srcdoc={text} sandbox="allow-scripts">。
 *                iframe 运行在不透明源里：脚本能执行（图表库 / 原生 SVG JS 能
 *                跑），但 cookie / localStorage / 访问父页面 / 顶层跳转 /
 *                弹窗 / 表单都被拦掉，因为刻意不包含 `allow-same-origin`。
 *   - text     : 抓取文本，若扩展名能映射到已知的 hljs 语言就用 lowlight 高亮；
 *                否则就是纯 <pre>。
 *
 * 媒体类型直接从 CloudFront 签名的 `download_url` 加载。文本类型则走
 * `/api/attachments/{id}/content`，以避开 CloudFront 的 CORS（未配置）和
 * Content-Disposition: attachment（强制下载头）。
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import {
  PreviewTooLargeError,
  PreviewUnsupportedError,
} from "@multica/core/api";
import {
  ChevronLeft,
  ChevronRight,
  Download,
  ExternalLink,
  FileText,
  Loader2,
  X,
} from "lucide-react";
import type { Attachment } from "@multica/core/types";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import {
  UI_EASE_OUT,
  UI_MOTION_DURATION,
} from "@multica/ui/lib/motion";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";
import { openExternal } from "../platform";
import { ReadonlyContent } from "./readonly-content";
import {
  extensionToLanguage,
  getPreviewKind,
  type PreviewKind,
} from "./utils/preview";
import { useDownloadAttachment } from "./use-download-attachment";
import { useAttachmentHtmlText } from "./hooks/use-attachment-html-text";
import { useResignedInlineMediaURL } from "./hooks/use-inline-media-url";
import { useZoomCanvas, type ZoomCanvasApi } from "./hooks/use-zoom-canvas";
import { ZoomCanvas, ZoomControls } from "./zoom-canvas";
import type { Size } from "./utils/zoom-transform";
import { HtmlPreviewBody } from "./html-preview-body";
import { CodeBlockStatic } from "./code-block-static";

// ---------------------------------------------------------------------------
// Preview source — 完整附件，或仅 URL（只支持媒体类型）
// ---------------------------------------------------------------------------
//
// `full` 携带已解析的 Attachment 记录，支持所有 PreviewKind（文本类型需要
// attachment id 去调 /api/attachments/{id}/content）。
//
// `url` 只携带签名 URL + 文件名。它是 NodeView 在 `resolveAttachment(href)`
// 返回 undefined 时的兜底——典型场景是 URL 被跨评论复制粘贴过，导致附件记录
// 无法从当前实体的 `attachments` prop 里找到。只有媒体类型（pdf / video /
// audio）能从 `url` 源打开，因为它们直接从 URL 渲染、不用经过文本内容代理。

/** 预览来源：完整附件记录，或仅 URL 引用（后者只支持媒体类型）。 */
export type PreviewSource =
  | { kind: "full"; attachment: Attachment }
  | { kind: "url"; url: string; filename: string };

// 只凭 URL 就能渲染的 PreviewKind。文本类（markdown / html / text）需要按 id
// 寻址的 /content 代理。
const URL_ONLY_KINDS = new Set<PreviewKind>(["image", "pdf", "video", "audio"]);

// `useAttachmentPreview` 下游统一使用的归一化视图。`attachmentId === null`
// 表示仅 URL 模式（下载回退到 `openExternal`，文本渲染分支被门控挡住、不可达）。
interface PreviewState {
  filename: string;
  contentType: string;
  mediaUrl: string;
  attachmentId: string | null;
}

function resolvePreviewMediaUrl(attachment: Attachment): string {
  const raw =
    attachment.download_url || attachment.markdown_url || attachment.url;
  return resolvePublicFileUrl(raw) ?? raw;
}

function normalize(source: PreviewSource): PreviewState {
  // 把任何服务器相对 URL（比如未配置 CloudFront 签名器时，统一端点元数据路径
  // 返回的 `/api/attachments/{id}/download`）解析到已配置的 API 基地址上。Web
  // 端默认基地址为空，保持相对路径并相对页面源解析——和本 PR 之前行为一致。
  // Desktop 渲染器（从 `app://` / file: / dev-server 源加载）需要绝对形式，好让
  // `<img src>` / `<iframe src>` / `<video src>` 真正指向 API 服务器而不是 shell
  // 源。
  if (source.kind === "full") {
    return {
      filename: source.attachment.filename,
      contentType: source.attachment.content_type,
      mediaUrl: resolvePreviewMediaUrl(source.attachment),
      attachmentId: source.attachment.id,
    };
  }
  return {
    filename: source.filename,
    contentType: "",
    mediaUrl: resolvePublicFileUrl(source.url) ?? source.url,
    attachmentId: null,
  };
}

// ---------------------------------------------------------------------------
// Public props
// ---------------------------------------------------------------------------

/**
 * 这个预览在某个界面的图片序列里的位置（MUL-5752）。
 *
 * 在边界处 `onPrev` / `onNext` 是 undefined —— 序列不循环，所以第一张/最后一张
 * 只是把对应的控制按钮禁用掉。只有 `ImageSequenceProvider` 会提供它；独立预览
 * 则不设置这个字段，渲染行为和以前完全一样。
 */
export interface PreviewSequence {
  /** 从 0 开始。渲染时显示为 `index + 1` / `total`。 */
  index: number;
  total: number;
  onPrev?: () => void;
  onNext?: () => void;
}

interface AttachmentPreviewModalProps {
  source: PreviewSource;
  open: boolean;
  onClose: () => void;
  sequence?: PreviewSequence;
  /** 图片类型加载失败时触发——让画廊能跳过这一帧。 */
  onImageError?: () => void;
}

// ---------------------------------------------------------------------------
// Hook — 本地状态 + 可直接挂载的弹窗 JSX
// ---------------------------------------------------------------------------
//
// 为什么不用 React context / provider：packages/views/ 无法在 CoreProvider
// （位于 packages/core/）里挂 Context.Provider，而为一个「同时最多只开一个弹窗」
// 的功能去给每个 app 布局都串一个新 provider，摩擦太大不划算。所以每个入口点
// 各自持有本地状态，渲染返回的 `modal` 节点。多个入口点共存只是各自带各自的
// （折叠态）状态——它们永远不会冲突，因为用户每次点击只会打开一个预览。

export interface AttachmentPreviewHandle {
  /** 尝试为这个 source 打开预览。当文件类型不可预览、或 source 是仅 URL 但
   *  类型又需要完整附件（text/markdown/html）时返回 false。调用方可回退到
   *  下载流程。 */
  tryOpen: (source: PreviewSource) => boolean;
  /** 强制打开预览，跳过 previewable() 的守卫。用于调用方已经提前过滤过的场景。 */
  open: (source: PreviewSource) => void;
  /** 要在调用方组件树某处渲染的弹窗节点。没有激活的预览时解析为 `null`。
   *  渲染在任意容器里都安全——弹窗会 portal 到 document.body。 */
  modal: ReactNode;
}

/**
 * 附件预览 hook：持有「当前要预览的内容」与「是否打开」的本地状态，并返回一个
 * 可直接渲染的弹窗节点。
 *
 * @returns 一个 { open, tryOpen, modal } 句柄，其中 modal 是要挂到组件树里的弹窗 JSX。
 */
export function useAttachmentPreview(): AttachmentPreviewHandle {
  const [current, setCurrent] = useState<PreviewSource | null>(null);
  const [previewOpen, setPreviewOpen] = useState(false);

  const open = useCallback((source: PreviewSource) => {
    setCurrent(source);
    setPreviewOpen(true);
  }, []);
  const tryOpen = useCallback((source: PreviewSource) => {
    const state = normalize(source);
    const kind = getPreviewKind(state.contentType, state.filename);
    if (!kind) return false;
    // 仅 URL 的 source 驱动不了文本类——/content 代理是按 id 寻址的。
    if (source.kind === "url" && !URL_ONLY_KINDS.has(kind)) return false;
    setCurrent(source);
    setPreviewOpen(true);
    return true;
  }, []);

  const modal = useMemo(
    () =>
      current ? (
        <AttachmentPreviewModal
          source={current}
          open={previewOpen}
          onClose={() => setPreviewOpen(false)}
          onExitComplete={() => setCurrent(null)}
        />
      ) : null,
    [current, previewOpen],
  );

  return useMemo(() => ({ open, tryOpen, modal }), [open, tryOpen, modal]);
}

// ---------------------------------------------------------------------------
// 无白屏地切换图片
// ---------------------------------------------------------------------------

// 返回「上一次解码完成」的图片 URL，在下一张下载期间把上一张保留在屏幕上。
// 如果导航发生的那一刻就换掉 `<img src>`（或重新挂载面板），画布会在整个
// 网络+解码间隙里白屏；「先解码、再切换」是灯箱的标准修法。
//
// 加载失败时 hook 上报错误并保留上一帧好图——当剩余序列全都坏了，读者会停留在
// 最后一张能显示的图片上（伴随「不可用」toast），而不是一个损坏的占位。
//
// 没有 `Image.decode()` 的环境（测试里的 jsdom）会立即切换：这是 MUL-5752 之前
// 的旧行为，在那里用正确性换回了它。
function useSettledImageURL(
  targetUrl: string,
  enabled: boolean,
  onLoadError?: () => void,
): string {
  const [settled, setSettled] = useState(targetUrl);
  const onErrorRef = useRef(onLoadError);
  onErrorRef.current = onLoadError;

  useEffect(() => {
    if (!enabled) return;
    if (!targetUrl) {
      setSettled(targetUrl);
      return;
    }
    let cancelled = false;
    const probe = new window.Image();
    if (typeof probe.decode !== "function") {
      setSettled(targetUrl);
      return;
    }
    probe.src = targetUrl;
    probe.decode().then(
      () => {
        if (!cancelled) setSettled(targetUrl);
      },
      () => {
        // 被拒绝既可能是加载失败，也可能是字节无法解码，两种情况都算出错。
        if (!cancelled) onErrorRef.current?.();
      },
    );
    return () => {
      cancelled = true;
    };
  }, [targetUrl, enabled]);

  return enabled ? settled : targetUrl;
}

/**
 * 为序列里的「相邻」图片预热浏览器缓存，让翻到它时切换无可见等待：它会先做面板
 * 自己也会做的那套 URL 重新签名，再通过一个游离的 <img> 抓取字节。本身不渲染
 * 任何东西。
 *
 * @param props.source - 要预取的那张图的预览来源
 */
export function PreviewImagePrefetch({ source }: { source: PreviewSource }) {
  const state = normalize(source);
  const url = useResignedInlineMediaURL(
    state.attachmentId ?? undefined,
    state.mediaUrl,
    true,
  );

  useEffect(() => {
    if (!url) return;
    const probe = new window.Image();
    probe.src = url;
  }, [url]);

  return null;
}

// ---------------------------------------------------------------------------
// Modal — frame + dispatch
// ---------------------------------------------------------------------------

/**
 * 附件全屏预览弹窗：按类型渲染图片 / PDF / 视频 / 音频 / 文本等内容，并提供
 * 关闭、下载、序列翻页等通用操作。
 *
 * @param props.source - 要预览的附件来源
 * @param props.open - 是否处于打开状态
 * @param props.onClose - 关闭回调
 * @param props.onExitComplete - 退出动画完成后的回调（清空当前内容）
 * @param props.sequence - 图片序列信息（用于前后翻页）
 * @param props.onImageError - 图片加载失败回调
 */
export function AttachmentPreviewModal({
  source,
  open,
  onClose,
  onExitComplete,
  sequence,
  onImageError,
}: AttachmentPreviewModalProps & { onExitComplete?: () => void }) {
  const download = useDownloadAttachment();
  const shouldReduceMotion = useReducedMotion() ?? false;
  const state = normalize(source);
  // 用 useWorkspaceSlug（而不是 useWorkspacePaths）——它在工作区路由之外返回
  // null 而不是抛异常，这样「新标签打开」按钮就直接隐藏。
  const slug = useWorkspaceSlug();
  const navigation = useNavigation();

  const onPrev = sequence?.onPrev;
  const onNext = sequence?.onNext;

  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
        return;
      }
      // 只有这个预览属于某个序列时才响应方向键翻页。此时缩放画布会让出它的水平
      // 方向键（见下面的 `horizontalArrowPan`），所以两者中恰好只有一方响应。
      // 带修饰键的按键仍交给浏览器 / 操作系统。
      if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return;
      if (e.key === "ArrowLeft" && onPrev) {
        e.preventDefault();
        onPrev();
      } else if (e.key === "ArrowRight" && onNext) {
        e.preventDefault();
        onNext();
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [open, onClose, onPrev, onNext]);

  const kind = getPreviewKind(state.contentType, state.filename);

  // 下载分发：有 id 时通过 `getAttachment` 重新签名；否则回退到外部打开那个
  // （可能已过期的）URL——和 file-card NodeView 下载路径的取舍一致。
  const handleDownload = () => {
    if (state.attachmentId) {
      download(state.attachmentId);
    } else {
      openExternal(state.mediaUrl);
    }
  };

  // 「新标签打开」对齐 HtmlAttachmentPreview 的内联工具栏：只有 `html` 类型有
  // 专属的全页路由（/attachments/{id}/preview）。同样用 slug + attachmentId
  // 做门控——仅 URL 的 source 无法寻址该页面依赖的 /content 代理。
  const canOpenInNewTab = kind === "html" && !!slug && !!state.attachmentId;
  const handleOpenInNewTab = () => {
    if (!slug || !state.attachmentId) return;
    const nameQuery = state.filename
      ? `?name=${encodeURIComponent(state.filename)}`
      : "";
    const path = `${paths.workspace(slug).attachmentPreview(state.attachmentId)}${nameQuery}`;
    const url = navigation.getShareableUrl(path);
    window.open(url, "_blank", "noopener,noreferrer");
    onClose();
  };

  if (typeof document === "undefined") return null;

  return createPortal(
    <AnimatePresence onExitComplete={onExitComplete}>
      {open && (
        <motion.div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4"
          // 只有落在背景遮罩自身上的点击才会关闭。从缩放画布开始、在外面松开的
          // 拖拽会通过指针捕获把点击重新指向这里，但这里显式声明意图，而不是依赖
          // 那个行为。
          onClick={(e) => {
            if (e.target === e.currentTarget) onClose();
          }}
          role="dialog"
          aria-modal="true"
          aria-label={state.filename}
          initial={{ opacity: 0 }}
          animate={{
            opacity: 1,
            transition: {
              duration: UI_MOTION_DURATION.fast,
              ease: UI_EASE_OUT,
            },
          }}
          exit={{
            opacity: 0,
            transition: {
              duration: UI_MOTION_DURATION.fast,
              ease: UI_EASE_OUT,
            },
          }}
        >
          {/* 比「新建工单」弹窗（max-w-4xl, manualDialogContentClass）更大，因为
              PDF / 视频预览需要更多空间。上限是视口减去四周的 p-4（每边 1rem），
              这样在小屏 / 分屏时也不会溢出屏幕。 */}
          <motion.div
            className="flex h-[min(90vh,calc(100vh-2rem))] w-full max-w-6xl flex-col overflow-hidden rounded-lg bg-background shadow-xl"
            onClick={(e) => e.stopPropagation()}
            initial={{
              opacity: 0,
              transform: shouldReduceMotion ? "scale(1)" : "scale(0.95)",
            }}
            animate={{
              opacity: 1,
              transform: "scale(1)",
              transition: {
                duration: UI_MOTION_DURATION.standard,
                ease: UI_EASE_OUT,
              },
            }}
            exit={{
              opacity: 0,
              transform: shouldReduceMotion ? "scale(1)" : "scale(0.95)",
              transition: {
                duration: UI_MOTION_DURATION.fast,
                ease: UI_EASE_OUT,
              },
            }}
          >
            {/* 故意放在 `open &&` 门控下面：面板的缩放状态在关闭时被销毁，所以每次
                打开都会重新适配，而不是恢复上一次看这张图时的过期缩放值。

                刻意不把面板 key 到文件上：序列翻页时重挂载面板会让画布在整个
                网络+解码间隙里白屏。面板保持挂载，只在下一张解码完成后才换图
                （`useSettledImageURL`）。缩放仍会按每张图重置——`natural` 在每次
                切换时都会先穿过 null，所以即便是一连串同分辨率的截图，画布也会
                重新适配。 */}
            <PreviewPanel
              kind={kind}
              source={source}
              state={state}
              onClose={onClose}
              onDownload={handleDownload}
              onOpenInNewTab={canOpenInNewTab ? handleOpenInNewTab : undefined}
              sequence={sequence}
              onImageError={onImageError}
            />
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>,
    document.body,
  );
}

// ---------------------------------------------------------------------------
// Panel — 头部 + 内容区
// ---------------------------------------------------------------------------

// 头部外壳和内容区放在一起，是因为图片类型的缩放控件在头部，而它们驱动的画布
// 在主体：共享状态需要单一所有者，随打开的弹窗一起挂载、销毁。
function PreviewPanel({
  kind,
  source,
  state,
  onClose,
  onDownload,
  onOpenInNewTab,
  sequence,
  onImageError,
}: {
  kind: PreviewKind | null;
  source: PreviewSource;
  state: PreviewState;
  onClose: () => void;
  onDownload: () => void;
  onOpenInNewTab?: () => void;
  sequence?: PreviewSequence;
  onImageError?: () => void;
}) {
  const { t } = useT("editor");

  // 画廊翻页会把这个面板交给一张读者从没点过的附件，所以——不像点击直通路径里
  // <Attachment> 已经升级过 URL——这里弹窗必须自己执行重新签名。对于本来就能
  // 加载的 URL（已签名的 CDN、公开存储）则是空操作。
  const targetUrl = useResignedInlineMediaURL(
    state.attachmentId ?? undefined,
    state.mediaUrl,
    kind === "image",
  );
  // 上一张图片会一直留在画布上直到这张解码完成——以前会闪的就是切换那一下。
  // 同时也能吸收「原始 URL → 签名 URL」的升级，不会出现第二次可见加载。
  const mediaUrl = useSettledImageURL(targetUrl, kind === "image", onImageError);

  // 原始尺寸跟着它被测量时的那张 URL 一起保存，这样复用了同一个面板去显示不同
  // 附件时，绝不会拿旧图尺寸去适配新图。
  const [measured, setMeasured] = useState<{ url: string; size: Size } | null>(
    null,
  );
  const natural =
    kind === "image" && measured?.url === mediaUrl ? measured.size : null;
  // 有序列时，左右方向键归序列翻页；否则画布自己留着做平移。垂直方向键始终
  // 平移，放大后的图片也仍能通过拖拽 / 滚轮水平平移。
  const canvas = useZoomCanvas({
    content: natural,
    horizontalArrowPan: !sequence,
  });

  const handleNaturalSize = useCallback(
    (url: string, size: Size) => {
      setMeasured((previous) =>
        previous?.url === url &&
        previous.size.width === size.width &&
        previous.size.height === size.height
          ? previous
          : { url, size },
      );
    },
    [],
  );

  return (
    <>
      <div className="flex items-center gap-2 border-b border-border bg-muted/30 px-4 py-2">
        <FileText className="size-4 shrink-0 text-muted-foreground" />
        {/* 基线对齐组：文件名（text-body）和类型（text-caption）字号不同，却挤在
            同一行——如果整行用 items-center 居中，会把它们高度不等的行盒居中对齐，
            让较小的文字明显偏移。混排字号文字应按基线对齐。 */}
        <div className="flex min-w-0 items-baseline gap-2">
          <p className="truncate text-body font-medium">{state.filename}</p>
          <span className="shrink-0 text-caption text-muted-foreground">
            {state.contentType || "—"}
          </span>
        </div>
        <div className="ml-auto flex items-center gap-1">
          {/* 导航排在整个操作区最前面，箭头移出图片（否则正好盖住正在看的内容），
              计数器放在它描述的两个箭头之间。min-w 保证数字位数变化时箭头不会
              左右移动。 */}
          {sequence && (
            <div className="mr-1 flex shrink-0 items-center gap-0.5">
              <SequenceButton
                side="prev"
                label={t(($) => $.image.previous)}
                onClick={sequence.onPrev}
              />
              <span
                className="min-w-10 select-none text-center text-caption tabular-nums text-muted-foreground"
                aria-live="polite"
              >
                {t(($) => $.image.sequence_position, {
                  index: sequence.index + 1,
                  total: sequence.total,
                })}
              </span>
              <SequenceButton
                side="next"
                label={t(($) => $.image.next)}
                onClick={sequence.onNext}
              />
            </div>
          )}
          {/* 独立预览保留原来的门控——图片测量出尺寸之前不显示控件，而对于没有
              固有尺寸可驱动的（非图片）内容则完全不显示。在序列里它们保持挂载
              （未测量时置灰）而不是消失重现：因为 `natural` 在每次切换时都会先
              穿过 null，控件若反复消失再出现，每次翻页都会把右侧按钮挤得左右
              跳动。 */}
          {kind === "image" && (natural || sequence) && (
            <ZoomControls canvas={canvas} disabled={!natural} />
          )}
          {onOpenInNewTab && (
            <button
              type="button"
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
              title={t(($) => $.attachment.open_in_new_tab)}
              aria-label={t(($) => $.attachment.open_in_new_tab)}
              onClick={onOpenInNewTab}
            >
              <ExternalLink className="size-4" />
            </button>
          )}
          <button
            type="button"
            className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
            title={t(($) => $.image.download)}
            aria-label={t(($) => $.image.download)}
            onClick={onDownload}
          >
            <Download className="size-4" />
          </button>
          <button
            type="button"
            className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
            title={t(($) => $.attachment.close)}
            aria-label={t(($) => $.attachment.close)}
            onClick={onClose}
          >
            <X className="size-4" />
          </button>
        </div>
      </div>
      {/* 图片用 flex 列：画布靠 `flex: 1 1 auto` 撑开自身，且它的内容是绝对定位，
          所以在普通块级父容器里会塌缩成零高度、什么都不显示。它还要自己裁剪并
          处理滚轮事件——若让这个外层也滚动，会和平移手势打架。其它所有类型都保留
          块级滚动；把它们变成 flex 子项会让较高的文本预览「缩小去适应」而不是
          滚动。 */}
      <div
        className={cn(
          "relative min-h-0 flex-1 bg-background",
          kind === "image" ? "flex flex-col overflow-hidden" : "overflow-auto",
        )}
      >
        {kind === "image" ? (
          <ImagePreview
            state={state}
            mediaUrl={mediaUrl}
            canvas={canvas}
            natural={natural}
            onNaturalSize={handleNaturalSize}
            onError={onImageError}
          />
        ) : (
          <PreviewContent
            kind={kind}
            source={source}
            state={state}
            onDownload={onDownload}
          />
        )}
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// 序列控件
// ---------------------------------------------------------------------------

// 头部的翻页箭头，和下载/关闭按钮同一套风格。`onClick` 为 undefined 表示「到
// 边界了」：按钮保持挂载但禁用，这样读者能看到自己已经到某一端，而不是控件消失、
// 计数器被挤到它的位置。用 `enabled:hover` 让禁用态没有悬停反馈（也不用
// pointer-events-none——禁用的控件仍应接住光标，读作「这里没有了」）。
function SequenceButton({
  side,
  label,
  onClick,
}: {
  side: "prev" | "next";
  label: string;
  onClick?: () => void;
}) {
  const Icon = side === "prev" ? ChevronLeft : ChevronRight;
  return (
    <button
      type="button"
      className="rounded-md p-1.5 text-muted-foreground transition-colors enabled:hover:bg-secondary enabled:hover:text-foreground disabled:opacity-30"
      title={label}
      aria-label={label}
      disabled={!onClick}
      onClick={onClick}
    >
      <Icon className="size-4" />
    </button>
  );
}

// ---------------------------------------------------------------------------
// Image — zoom canvas
// ---------------------------------------------------------------------------

function ImagePreview({
  state,
  mediaUrl,
  canvas,
  natural,
  onNaturalSize,
  onError,
}: {
  state: PreviewState;
  mediaUrl: string;
  canvas: ZoomCanvasApi;
  natural: Size | null;
  onNaturalSize: (url: string, size: Size) => void;
  onError?: () => void;
}) {
  const { t } = useT("editor");
  const url = mediaUrl;

  const readNaturalSize = useCallback(
    (image: HTMLImageElement | null) => {
      // naturalWidth 在图片还没解码完时为 0；只声明了 viewBox 的 SVG 也是 0
      // ——Chromium 压根不给它们固有尺寸。两种情况都回退到信箱（letterbox）分支；
      // 前者加载后能恢复，后者就一直停在那里。
      if (!image || image.naturalWidth <= 0 || image.naturalHeight <= 0) return;
      onNaturalSize(url, {
        width: image.naturalWidth,
        height: image.naturalHeight,
      });
    },
    [onNaturalSize, url],
  );

  return (
    <ZoomCanvas
      canvas={canvas}
      content={natural}
      label={t(($) => $.image.canvas_label)}
      className="bg-black/40"
      autoFocus
    >
      <img
        // 缓存的图片在 React 挂上 onLoad 之前就已经是 `complete` 状态了，所以
        // 那个事件根本不会触发——因此也要通过 ref 来测量。
        ref={readNaturalSize}
        onLoad={(e) => readNaturalSize(e.currentTarget)}
        onError={onError}
        src={url}
        alt={state.filename}
        className={cn(
          "select-none",
          natural
            ? "block size-full"
            : "max-h-full max-w-full rounded-lg object-contain",
        )}
        // 原生的图片拖拽会劫持平移手势。
        draggable={false}
      />
    </ZoomCanvas>
  );
}

// ---------------------------------------------------------------------------
// 分发
// ---------------------------------------------------------------------------

// 按 PreviewKind 分发。新类型都加在这里；记住弹窗外框（头部、关闭、下载按钮、
// ESC 处理）是共享的——子渲染器只负责内容区。`image` 由 PreviewPanel 自己处理，
// 因为它的工具栏和画布共享缩放状态。
function PreviewContent({
  kind,
  source,
  state,
  onDownload,
}: {
  kind: Exclude<PreviewKind, "image"> | null;
  source: PreviewSource;
  state: PreviewState;
  onDownload: () => void;
}) {
  const { t } = useT("editor");

  if (kind === null) {
    return (
      <UnsupportedFallback
        message={t(($) => $.attachment.preview_unsupported)}
        onDownload={onDownload}
      />
    );
  }

  // 文本类需要 attachment id 才能走 /content 代理。tryOpen 的门控会挡住文本类的
  // 仅 URL source，但这里还是防御一下——否则直接用 URL source 挂载
  // <AttachmentPreviewModal>，且其文件名后来解析成文本类时，会在 null id 上崩溃。
  if (
    (kind === "markdown" || kind === "html" || kind === "text") &&
    !state.attachmentId
  ) {
    return (
      <UnsupportedFallback
        message={t(($) => $.attachment.preview_unsupported)}
        onDownload={onDownload}
      />
    );
  }

  switch (kind) {
    case "pdf":
      return (
        <iframe
          src={state.mediaUrl}
          className="h-full w-full bg-background"
          title={state.filename}
        />
      );
    case "video":
      return (
        <div className="flex h-full w-full items-center justify-center bg-black">
          <video
            src={state.mediaUrl}
            controls
            className="h-full w-full object-contain"
          />
        </div>
      );
    case "audio":
      return (
        <div className="flex h-full w-full items-center justify-center p-8">
          <audio src={state.mediaUrl} controls className="w-full max-w-xl" />
        </div>
      );
    case "markdown":
      return (
        <TextBackedPreview
          attachmentId={state.attachmentId!}
          onDownload={onDownload}
          render={(text) => (
            <ReadonlyContent
              content={text}
              className="px-6 py-4"
              attachments={source.kind === "full" ? [source.attachment] : []}
            />
          )}
        />
      );
    case "html":
      return (
        <TextBackedPreview
          attachmentId={state.attachmentId!}
          onDownload={onDownload}
          render={(text) => (
            <HtmlPreviewBody
              source={{ kind: "inline", html: text }}
              title={state.filename}
              className="h-full w-full"
              iframeClassName="rounded-none border-0"
            />
          )}
        />
      );
    case "text":
      return (
        <TextBackedPreview
          attachmentId={state.attachmentId!}
          onDownload={onDownload}
          render={(text) => (
            <CodeBlockStatic
              language={extensionToLanguage(state.filename)}
              body={text}
              className="px-6 py-4"
            />
          )}
        />
      );
  }
}

// ---------------------------------------------------------------------------
// 文本类预览 — 抓取一次正文，再交给 render prop
// ---------------------------------------------------------------------------

// 按项目约定，服务端状态归 React Query 管；重复打开同一个附件会命中缓存而不是
// 重新抓取。查询只以 attachment id 为 key——服务端签名 URL 的 30 分钟 TTL 比任何
// 可想象的预览会话都长得多。
function TextBackedPreview({
  attachmentId,
  onDownload,
  render,
}: {
  attachmentId: string;
  onDownload: () => void;
  render: (text: string) => ReactNode;
}) {
  const { t } = useT("editor");
  const query = useAttachmentHtmlText(attachmentId);

  if (query.isLoading) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-body text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
        {t(($) => $.attachment.preview_loading)}
      </div>
    );
  }
  if (query.error) {
    if (query.error instanceof PreviewTooLargeError) {
      return (
        <UnsupportedFallback
          message={t(($) => $.attachment.preview_too_large)}
          onDownload={onDownload}
        />
      );
    }
    if (query.error instanceof PreviewUnsupportedError) {
      return (
        <UnsupportedFallback
          message={t(($) => $.attachment.preview_unsupported)}
          onDownload={onDownload}
        />
      );
    }
    return (
      <UnsupportedFallback
        message={t(($) => $.attachment.preview_failed)}
        onDownload={onDownload}
      />
    );
  }
  if (!query.data) return null;
  return <>{render(query.data.text)}</>;
}

// ---------------------------------------------------------------------------
// 兜底 — 用于 413 / 415 / 未知类型
// ---------------------------------------------------------------------------

function UnsupportedFallback({
  message,
  onDownload,
}: {
  message: string;
  onDownload: () => void;
}) {
  const { t } = useT("editor");
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-8 text-center">
      <FileText className="size-8 text-muted-foreground" />
      <p className="text-body text-muted-foreground">{message}</p>
      <button
        type="button"
        className="inline-flex items-center gap-2 rounded-md border border-border bg-background px-3 py-1.5 text-body transition-colors hover:bg-muted"
        onClick={onDownload}
      >
        <Download className="size-4" />
        {t(($) => $.image.download)}
      </button>
    </div>
  );
}

// 从工具模块再导出「是否可预览」的判断函数，让各入口组件只用一个 import 就能
// 门控「眼睛（预览）」按钮。
export { isPreviewable } from "./utils/preview";
