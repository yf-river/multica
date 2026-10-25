"use client";

/**
 * ImageSequenceProvider——单个 surface 内图片的「上一张/下一张」（MUL-5752）。
 *
 * 一个能容纳多张图的 surface（issue：描述 + 每条评论；聊天会话：每条消息）
 * 挂载一次本组件，序列由 `collectImageSequence` 构建。点击其中任何图片都会
 * 在该图的真实位置打开共享预览弹窗，读者可翻看其余图片。
 *
 * 为什么用一个 Provider 而不是每图状态：`<Attachment>` 拥有私有的
 * `useAttachmentPreview()` 弹窗——对孤图是正确的，但它无从知道下一张是什么。
 * Provider 在所有图片之上托管唯一一个弹窗，导航状态因此只有一个属主。
 *
 * 两个值得先说清的行为，都来自产品 brief：
 *
 *   - 弹窗打开时序列被**冻结**。预览打开期间新评论与流式 agent 输出仍在
 *     到达；实时重算会让读者看着看着「3 / 7」就变了。
 *   - 边界是**禁用**而不是循环。第一张：没有上一张。最后一张：没有下一张。
 *
 * 序列之外的图片（composer 里正在上传的占位图、没有 Provider 的 surface 里
 * 的图）不是错误：`openAt` 返回 false，调用方退回自己的单图预览。
 */

import {
  createContext,
  use,
  useCallback,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { toast } from "sonner";
import {
  indexOfImageKey,
  type ImageSequenceItem,
} from "@multica/core/attachments/image-sequence";
import { useT } from "../i18n";
import {
  AttachmentPreviewModal,
  PreviewImagePrefetch,
  type PreviewSource,
} from "./attachment-preview-modal";

interface ImageSequenceApi {
  /**
   * 在 `key` 处打开共享查看器——key 是附件 id；对解析不到记录的引用则是
   * 正文里原样写着的 URL。
   *
   * 没有挂载 Provider、或该 key 不属于本 surface 序列时返回 false，调用方
   * 可退回单图预览。
   */
  openAt: (key: string) => boolean;
}

const NO_SEQUENCE: ImageSequenceApi = { openAt: () => false };

const ImageSequenceContext = createContext<ImageSequenceApi>(NO_SEQUENCE);

/**
 * 取得所在 surface 的图片查看器；没有 Provider 时拿到一个 no-op 句柄。
 * 任何情况下都可安全调用——`openAt` 返回 false 是文档化的「不在序列内」
 * 答案，不是失败。
 */
export function useImageSequencePreview(): ImageSequenceApi {
  return use(ImageSequenceContext);
}

/** 把序列条目转成预览弹窗的 source。 */
function toPreviewSource(item: ImageSequenceItem): PreviewSource {
  return item.attachment
    ? { kind: "full", attachment: item.attachment }
    : // Everything in a sequence is an image by construction —
      // `collectImageSequence` admits nothing else — so the viewer says so
      // rather than letting the modal re-derive it from `filename`. For an
      // item with no attachment record that field holds the markdown caption
      // (`![报告图表](…)`), which is prose and has no extension to read
      // (MUL-7518).
      {
        kind: "url",
        url: item.url,
        filename: item.filename,
        forceKind: "image",
      };
}

/** 一次打开的会话快照。 */
interface Session {
  /** 打开那一刻的快照——见文件头「冻结」说明。 */
  items: ImageSequenceItem[];
  index: number;
}

/**
 * 图片序列 Provider：在打开的预览弹窗里支持按 key 打开与前后翻页。
 */
export function ImageSequenceProvider({
  items,
  children,
}: {
  items: ReadonlyArray<ImageSequenceItem>;
  children: ReactNode;
}) {
  const { t } = useT("editor");
  // 只在点击时刻读取，让流式 surface 不必在每条消息到达时都重建 context
  // 值（并重渲染每张图片）。
  const itemsRef = useRef(items);
  itemsRef.current = items;

  const [session, setSession] = useState<Session | null>(null);
  const [open, setOpen] = useState(false);
  // 本次会话里加载失败的图片。把它们排除在导航之外：被删附件不能把读者
  // 困死在坏帧上，下面的自动跳过也不会在两张死图之间永远弹跳。ref 是同步
  // 事实（<img> 在 React 重渲染前可能多次上报失败）；state 副本驱动那次
  // 重渲染。
  const brokenRef = useRef<Set<string>>(new Set());
  const [broken, setBroken] = useState<ReadonlySet<string>>(brokenRef.current);
  // 上次移动的方向，让一次加载失败能顺着读者原来的方向继续跳过。
  const directionRef = useRef<1 | -1>(1);

  const api = useMemo<ImageSequenceApi>(
    () => ({
      openAt: (key: string) => {
        const snapshot = [...itemsRef.current];
        const index = indexOfImageKey(snapshot, key);
        if (index < 0) return false;
        directionRef.current = 1;
        brokenRef.current = new Set();
        setBroken(brokenRef.current);
        setSession({ items: snapshot, index });
        setOpen(true);
        return true;
      },
    }),
    [],
  );

  /** 从 from 起按 delta 方向找第一个未损坏的图片下标。 */
  const step = useCallback(
    (current: Session, from: number, delta: 1 | -1, skip: ReadonlySet<string>) => {
      for (let i = from + delta; i >= 0 && i < current.items.length; i += delta) {
        if (!skip.has(current.items[i]!.key)) return i;
      }
      return -1;
    },
    [],
  );

  const go = useCallback(
    (delta: 1 | -1) => {
      if (!session) return;
      const next = step(session, session.index, delta, brokenRef.current);
      if (next < 0) return;
      directionRef.current = delta;
      setSession({ ...session, index: next });
    },
    [session, step],
  );

  // 解码失败的帧会被跳过而不是留在屏上：附件被删了，或其签名 URL 活过了
  // 本次会话。先朝读者本来的方向前进，再掉头；两头都没有可加载的图才放弃
  // （并留下坏帧可见）。
  const handleImageError = useCallback(() => {
    if (!session) return;
    const failed = session.items[session.index];
    if (!failed || brokenRef.current.has(failed.key)) return;

    brokenRef.current = new Set(brokenRef.current).add(failed.key);
    setBroken(brokenRef.current);
    toast.error(t(($) => $.image.unavailable));

    const forward = directionRef.current;
    const next = step(session, session.index, forward, brokenRef.current);
    const target =
      next >= 0
        ? next
        : step(session, session.index, forward === 1 ? -1 : 1, brokenRef.current);
    if (target >= 0) setSession({ ...session, index: target });
  }, [session, step, t]);

  const current = session ? session.items[session.index] : undefined;
  const prevIndex = session ? step(session, session.index, -1, broken) : -1;
  const nextIndex = session ? step(session, session.index, 1, broken) : -1;

  const modal =
    session && current ? (
      <AttachmentPreviewModal
        source={toPreviewSource(current)}
        open={open}
        onClose={() => setOpen(false)}
        onExitComplete={() => setSession(null)}
        onImageError={handleImageError}
        sequence={
          session.items.length > 1
            ? {
                index: session.index,
                total: session.items.length,
                onPrev: prevIndex >= 0 ? () => go(-1) : undefined,
                onNext: nextIndex >= 0 ? () => go(1) : undefined,
              }
            : undefined
        }
      />
    ) : null;

  return (
    <ImageSequenceContext.Provider value={api}>
      {children}
      {modal}
      {/* 预览打开期间预热相邻图片，让翻页直接命中缓存而不是等一次网络往返。
          用 key 控制挂载：翻页后 key 变化，预取自动改目标到新的相邻图。 */}
      {open && session && prevIndex >= 0 && (
        <PreviewImagePrefetch
          key={session.items[prevIndex]!.key}
          source={toPreviewSource(session.items[prevIndex]!)}
        />
      )}
      {open && session && nextIndex >= 0 && (
        <PreviewImagePrefetch
          key={session.items[nextIndex]!.key}
          source={toPreviewSource(session.items[nextIndex]!)}
        />
      )}
    </ImageSequenceContext.Provider>
  );
}
