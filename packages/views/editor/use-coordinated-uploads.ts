"use client";

/**
 * 每个 composer 界面共享的「协调上传」引擎（MUL-5181, L2）。
 *
 * 归属反转：一次上传归属于模块级上传协调器（`@multica/core/drafts`），
 * 而不是发起它的 React 组件。选择文件时引擎通过
 * {@link UploadDraftBinding} 立刻把持久化占位符写进界面的草稿，再把文件
 * 交给协调器。关掉或滚走 composer 不再中止上传；登出会中止所有被追踪的
 * 请求；加载时仍 `uploading` 的占位符会被 store 丢掉——字节从未持久化，
 * 既不能恢复也不能重试。
 *
 * `onSettled` 有代际守卫：它重读草稿，只有占位符仍被追踪才写入（请求在途
 * 时草稿可能已被提交或清空）。协调器在中止时绝不调用 `onSettled`，所以
 * 登出——先中止、后清草稿——不可能把占位符复活进一份被清空的草稿。
 *
 * 提交绑定的「事实来源」是草稿正文（引用过滤后）：一次在挂载死后才落定的
 * 上传，会把它的 markdown 链接送回到正文里——有重开的 composer 且其 Tiptap
 * 实例仍在预热时送进实时编辑器（确认 + 重试），否则追加到持久化草稿——文件
 * 因此可见、可删，删除它也确实解绑它。
 *
 * 界面用 {@link UploadDraftBinding} 接入：命令式、store 支撑的访问器，必须
 * 在组件卸载后仍可调用。绑定必须对每个目标引用稳定（按草稿 key memo）。
 */

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import {
  startUpload,
  abortUpload,
  hasUploadingDraft,
  attachmentToDraftUpload,
  type DraftUpload,
} from "@multica/core/drafts";
import { createSafeId } from "@multica/core/utils";
import { contentReferencesAttachment, type Attachment } from "@multica/core/types";
import {
  toUploadResult,
  type UploadContext,
  type UploadResult,
} from "@multica/core/hooks/use-file-upload";
import { MAX_FILE_SIZE } from "@multica/core/constants/upload";
import { useT } from "../i18n";
import type { UploadGate } from "./use-upload-gate";
import type { ContentEditorRef } from "./content-editor";
import { pastedTextSource } from "./extensions/file-upload";

const EMPTY_ATTACHMENTS: Attachment[] = [];

/**
 * 一个 composer 目标的上传与正文的 store 支撑访问器。每个方法都必须走
 * store 的 `getState()`（绝不捕获 React state）：settle 处理器会在属主组件
 * 消失后调用它们。
 */
export interface UploadDraftBinding {
  /**
   * 实时编辑器注册表的身份标识——每个 composer 目标唯一（如
   * `comment:new:{issueId}`、`issue-create:manual`）。同一目标的重开
   * composer 会把它的编辑器注册在同一个 key 下，settle 处理器据此找到它。
   */
  registryKey: string;
  getUploads: () => DraftUpload[];
  addUpload: (upload: DraftUpload) => void;
  settleUpload: (clientUploadId: string, attachment: Attachment) => void;
  failUpload: (clientUploadId: string, error?: string) => void;
  removeUpload: (clientUploadId: string) => void;
  /** 提交时刻引用过滤所绑定的草稿正文。 */
  getBody: () => string;
  /** 把一个 markdown 片段追加到持久化正文。 */
  appendToBody: (markdown: string) => void;
}

// 每个注册 key 当前展示的编辑器。让一个自己的挂载已消失的 settle 处理器
// （上传活过了 composer）把完成的链接交给「为同一目标重开」的编辑器。
const liveEditors = new Map<string, RefObject<ContentEditorRef | null>>();

/** 仅测试用：当前已注册的 key。让时序测试断言注册发生在 COMMIT（layout）
 *  阶段，而不是稍后的被动任务。 */
export function __liveEditorRegistryKeysForTest(): string[] {
  return [...liveEditors.keys()];
}

/** 一次已完成上传的 markdown。镜像编辑器内替换产生的形态
 *  （`extensions/file-upload.ts`：图片走 image 节点、其余走 fileCard 链接）——
 *  两者必须保持同步。 */
export function attachmentMarkdown(att: Attachment): string {
  const link = toUploadResult(att).markdownLink;
  return (att.content_type ?? "").startsWith("image/")
    ? `![${att.filename}](${link})`
    : `[${att.filename}](${link})`;
}

const DELIVER_RETRY_MS = 50;
const DELIVER_MAX_TRIES = 100; // 约 5s——编辑器初始化是被动 effect，离这个很远

/**
 * 在拥有上传的挂载死后，把已完成上传的 markdown 链接送进草稿正文。
 * 投递必须被**确认**而不能想当然：
 *
 *  - 该 key 有实时编辑器且插入成功 → 也经 `appendToBody` 持久化同一正文
 *    作为保险——编辑器防抖 emit 会在快速卸载时丢帧，而且两条写入反正会
 *    收敛到相同内容。
 *  - 没有 composer 挂载该 key → 追加到持久化草稿；下次挂载把它当
 *    `defaultValue` 读到。
 *  - composer 挂载了但 Tiptap 实例还没建（首次 commit 即有句柄；实例在
 *    被动 effect 里到）→ 重试。这里直接写 store 会被已挂载编辑器的首次
 *    emit 抹掉——那个 emit 快照的正文还没有这条链接。
 *
 * 每次尝试都会在写入前重查代际守卫（等待期间草稿可能被清空或提交）与正文
 * （链接可能已用别的方式落了地）。
 */
function deliverFinishedUpload(
  binding: UploadDraftBinding,
  clientUploadId: string,
  attachment: Attachment,
  tries = 0,
): void {
  if (!binding.getUploads().some((u) => u.clientUploadId === clientUploadId)) return;
  if (contentReferencesAttachment(binding.getBody(), attachment)) return;

  const md = attachmentMarkdown(attachment);
  const live = liveEditors.get(binding.registryKey);
  // 展示该目标的 composer 挂载时重建了占位符，所以完成的附件在用户上次
  // 看到它的位置**原位替换**，而不是在末尾再追加一次。
  if (live?.current?.settleUploadPlaceholder(clientUploadId, toUploadResult(attachment)) === true) {
    binding.appendToBody(md);
    return;
  }
  if (live?.current?.insertMarkdownAtEnd(md) === true) {
    binding.appendToBody(md);
    return;
  }
  if (!live) {
    binding.appendToBody(md);
    return;
  }
  if (tries >= DELIVER_MAX_TRIES) {
    // 编辑器始终没初始化——写入 store 是次优选项里最好的。
    binding.appendToBody(md);
    return;
  }
  setTimeout(
    () => deliverFinishedUpload(binding, clientUploadId, attachment, tries + 1),
    DELIVER_RETRY_MS,
  );
}

/**
 * 把失败的「粘贴成文件」的源文本放回用户能看到的地方。
 *
 * 这是 {@link deliverFinishedUpload} 的镜像，存在理由相同：上传活过挂载，
 * 失败落地时吞掉那次粘贴的 composer 可能已经没了。与拖放的文件不同，这份
 * 内容没有其它副本——它从未写进文档、来源页签可能已关——所以「编辑器没了
 * 就丢掉」会是静默的数据丢失。
 *
 * 以 markdown 而非字面文本恢复：若那次粘贴从未被转成文件，处理它的恰好会
 * 是 `markdown-paste`，因此这复现了用户本来会得到的东西。优先实时编辑器
 * （落点在文档末尾，绝不会插进用户后来移开的光标所在的句子中间），否则
 * 写持久化正文。
 */
function deliverPastedTextBack(
  binding: UploadDraftBinding | undefined,
  editorRef: RefObject<ContentEditorRef | null>,
  text: string,
): void {
  if (!binding) {
    // 没有持久化上下文（没有草稿 key 打开的回复 composer）：实时编辑器
    // 是唯一还能放的地方。
    editorRef.current?.insertMarkdownAtEnd(text);
    return;
  }
  // 与 deliverFinishedUpload 相同的保险：一次落地的编辑器插入仍可能因快速
  // 卸载丢掉防抖 emit，而两条写入都会收敛到相同内容。
  liveEditors.get(binding.registryKey)?.current?.insertMarkdownAtEnd(text);
  binding.appendToBody(text);
}

export interface CoordinatedUploads {
  /** 本 composer 的全部上传，含占位符。 */
  uploads: DraftUpload[];
  /** 已完成的附件行——编辑器的预览集合；提交绑定的是正文仍引用其链接的
   *  那个子集。 */
  attachments: Attachment[];
  /**
   * 接 `<ContentEditor onUploadFile={...} />`。
   *
   * 编辑器画占位节点时铸造 `uploadId` 并传进来，让文档节点与草稿记录共
   * 用一个 id——这正是抵达「并非它发起的挂载」的 settle 能再次找到节点的
   * 原因。这个第二参数要留在类型里：一个静默丢弃它的 mock 或手写调用方
   * 会铸造第二个 id 从而切断那条链路。仅对「没有编辑器占位符可配」的
   * 调用方是可选的。
   */
  handleUpload: (file: File, uploadId?: string) => Promise<UploadResult | null>;
  /** 移除一个占位符（解除失败/中断）。 */
  removeUpload: (clientUploadId: string) => void;
  /**
   * 本 composer 的提交门禁。把编辑器门禁（本次挂载的文档内上传）与草稿里
   * 协调器持有的占位符组合起来：在前一挂载的上传仍在途时重开的 composer
   * 文档是干净的，所以单靠编辑器门禁会让一次发送清空「上传中」小徽标还亮
   * 着的草稿——静默丢掉那个文件。
   */
  gate: UploadGate;
}

/**
 * 协调上传 hook。
 *
 * @param binding      持久化目标的 store 支撑访问器。缺省时（无持久化上下文
 *                    的 composer）上传退回组件本地状态、随挂载消亡，与 L2
 *                     之前行为一致。
 * @param boundUploads 绑定方上传的响应式版本（调用方的 store 订阅）。binding
 *                     缺省时忽略。
 */
export function useCoordinatedUploads(
  binding: UploadDraftBinding | undefined,
  boundUploads: DraftUpload[],
  ctx: UploadContext,
  editorGate: UploadGate,
  editorRef: RefObject<ContentEditorRef | null>,
  opts?: {
    /**
     * 解析一次「新上传」应指向的绑定，在选择文件那一刻快照。用于单个编辑器
     * 实例可持有与当前选中不同的草稿的 composer（聊天在上传在途时把文档钉在
     * 某条会话上）：文件落进编辑器**正持有**的文档，所以它的占位/落定/回写
     * 必须跟着那份草稿，而不是请求完成时恰好选中的那份。默认就是 `binding`。
     */
    resolveUploadTarget?: () => UploadDraftBinding;
    /**
     * 注册本次挂载编辑器所用的注册表 key，当它不同于 `binding.registryKey`
     * 时。与上面同源的分叉：一次回写只在「该编辑器文档属于正在落定的草稿」
     * 时才应插入，所以带可钉住编辑器的 composer 注册**已加载**的 key。
     */
    liveRegistryKey?: string;
  },
): CoordinatedUploads {
  const { t } = useT("editor");
  const [localUploads, setLocalUploads] = useState<DraftUpload[]>([]);
  // 最新值 ref：handleUpload 在调用时刻快照目标。
  const resolveUploadTargetRef = useRef(opts?.resolveUploadTarget);
  resolveUploadTargetRef.current = opts?.resolveUploadTarget;

  // 本次挂载的存活态，由它创建的 settle 闭包读取：true 时发起上传的编辑器
  // 自己会做内联替换；false 后回写是唯一能落地链接的路径。刻意用 layout
  // effect：React 会在卸载 commit 时把子编辑器 ref 置空，但被动 cleanup 要
  // 再晚一个任务才翻转——落在那段间隙里的 settle 会看到「已挂载」却没有
  // 可替换的编辑器。
  const mountedRef = useRef(true);
  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // 注册只对持久化目标存在；没有 binding 的 liveRegistryKey 会注册一个永远
  // 没人能查到的编辑器。
  const registryKey = binding ? (opts?.liveRegistryKey ?? binding.registryKey) : undefined;
  // 用 layout effect 与 mountedRef 同理：chat 的 adopt 会在 commit 期间同步
  // 替换编辑器文档（与已加载 key），若晚一个任务的被动重注册，会让旧 key 在
  // 某段 settle 窗口里仍映射着一个已持有另一草稿文档的编辑器。
  useLayoutEffect(() => {
    if (!registryKey) return;
    liveEditors.set(registryKey, editorRef);
    return () => {
      if (liveEditors.get(registryKey) === editorRef) liveEditors.delete(registryKey);
    };
  }, [registryKey, editorRef]);

  const uploads = binding ? boundUploads : localUploads;
  const attachments = useMemo(() => {
    const done: Attachment[] = [];
    for (const u of uploads) {
      if (u.status === "uploaded") done.push(u.attachment);
    }
    return done.length === 0 ? EMPTY_ATTACHMENTS : done;
  }, [uploads]);

  // 为「本文档没在展示的上传」重建占位符。
  //
  // 上传会活过发起它的挂载，但占位节点不会——占位符从不序列化进草稿正文，
  // 所以重开的 composer 开始时对仍在跑的上传毫无痕迹。草稿仍持有记录，足以
  // 重画它；settle 随后原位替换它（见 deliverFinishedUpload）。没有这一步，
  // composer 看起来空闲，而 `gate` 却在安静地挡着发送。
  //
  // 每个 id 每次挂载只重建一次，用这里（而非扫文档）跟踪：用户中途删掉
  // 占位符是认真的，MUL-5181 那条「删过的占位符保持删除」的规则不该被下次
  // store 写入重画出来抵消。
  //
  // 落不了地就重试，因为命令式句柄首次 commit 就有、而 Tiptap 实例晚一个
  // 被动 effect 才到——deliverFinishedUpload 也在重试同一段窗口。
  const rebuiltUploadIdsRef = useRef<Set<string>>(new Set());
  // chat 会把文档钉在「在途上传开始的草稿」上，而用户同时在浏览另一会话，
  // 所以 `uploads`（被选中的草稿）能指向与文档持有者不同的目标。那时再画
  // 会把一份草稿的占位符画进另一份草稿的文档。注册表 key 正是这个分叉的
  // 信号。
  const editorHoldsThisTarget = !binding || registryKey === binding.registryKey;
  useEffect(() => {
    if (!editorHoldsThisTarget) return;
    const pending = uploads.filter(
      (u) => u.status === "uploading" && !rebuiltUploadIdsRef.current.has(u.clientUploadId),
    );
    if (pending.length === 0) return;
    let cancelled = false;
    let tries = 0;
    const attempt = () => {
      if (cancelled) return;
      const missing = pending.filter((u) => {
        const landed = editorRef.current?.insertUploadPlaceholder({
          uploadId: u.clientUploadId,
          filename: u.filename,
          size: u.size,
        });
        if (landed === true) rebuiltUploadIdsRef.current.add(u.clientUploadId);
        return landed !== true;
      });
      if (missing.length === 0) return;
      if (++tries >= DELIVER_MAX_TRIES) return;
      setTimeout(attempt, DELIVER_RETRY_MS);
    };
    attempt();
    return () => {
      cancelled = true;
    };
  }, [uploads, editorRef, editorHoldsThisTarget]);

  const issueId = ctx.issueId;
  const commentId = ctx.commentId;
  const chatSessionId = ctx.chatSessionId;

  const handleUpload = useCallback(
    (file: File, uploadId?: string): Promise<UploadResult | null> => {
      // 采纳编辑器铸造的 id 而不是再造一个：文档节点与这条草稿记录是同一
      // 次上传，抵达「并非它发起的挂载」的 settle 只能按 id 找到节点。
      // 兜底覆盖没有编辑器占位符可配的调用方。
      const clientUploadId = uploadId ?? createSafeId();
      // 编辑器交进来的 id 意味着它**已经**画了节点，所以该上传从此刻起算
      // 已重建。现在注册而不是让 effect 去发现节点，能堵上两者之间的空档：
      // 那段窗口里 effect 会看不到节点（用户可能已删了它）而再画一个，复活
      // 一个用户已移除的占位符。窗口虽然不足一帧，但「谁画的谁注册」读起来
      // 比一个没人能撞上的竞态更顺。
      if (uploadId) rebuiltUploadIdsRef.current.add(uploadId);
      // 现在立刻快照目标：settle 处理器必须始终寻址文件落进去的那份草稿，
      // 无论它们触发时选中的是哪个。
      const target = binding ? (resolveUploadTargetRef.current?.() ?? binding) : undefined;
      const placeholder: DraftUpload = {
        clientUploadId,
        status: "uploading",
        filename: file.name,
        size: file.size,
        contentType: file.type || undefined,
      };

      const pastedText = pastedTextSource(file);

      if (file.size > MAX_FILE_SIZE) {
        // 既不进协调器也不进草稿——为什么失败不留占位符见下面的 settle
        // 处理器。
        const reason = "File exceeds 100 MB limit";
        if (pastedText !== undefined) {
          // 粘贴没有磁盘副本可重新挂载，所以文本回进 composer，而不是随
          // 上传一起丢失。
          deliverPastedTextBack(target, editorRef, pastedText);
        }
        toast.error(t(($) => $.upload.failed, { filename: file.name, reason }));
        return Promise.resolve(null);
      }

      if (target) {
        target.addUpload(placeholder);
      } else {
        setLocalUploads((prev) => [...prev, placeholder]);
      }

      return new Promise<UploadResult | null>((resolve) => {
        startUpload({
          clientUploadId,
          file,
          api,
          ctx: { issueId, commentId, chatSessionId },
          onSettled: (outcome) => {
            if (outcome.status === "uploaded") {
              if (target) {
                // 代际守卫：草稿仍追踪它才写入。
                if (target.getUploads().some((u) => u.clientUploadId === clientUploadId)) {
                  target.settleUpload(clientUploadId, outcome.attachment);
                  // 回写（MUL-5181）：发起这次上传的挂载已消失，没有编辑器
                  // 替换会把完成链接放进文档——改投进正文（提交绑定的是它，
                  // 引用过滤）。本次挂载还活着时跳过：下面 resolve promise
                  // 驱动正常的内联 blob→URL 替换，而用户中途删掉的占位符
                  // 必须保持已删。
                  if (!mountedRef.current) {
                    deliverFinishedUpload(target, clientUploadId, outcome.attachment);
                  }
                }
              } else {
                setLocalUploads((prev) =>
                  prev.map((u) =>
                    u.clientUploadId === clientUploadId
                      ? { ...attachmentToDraftUpload(outcome.attachment), clientUploadId }
                      : u,
                  ),
                );
              }
              resolve(toUploadResult(outcome.attachment));
            } else {
              const reason = outcome.error.message;
              // 一次失败什么都不留。下面的 toast 在发生那一刻已经说过了，
              // 且文件仍在磁盘上——小徽标不增加信息也无法重试（字节从未
              // 持久化）。留着它得不偿失：它跨刷新/重开存活直到手动消除，
              // 且 `isMeaningful` 会计入它，一次偶发抖动就能让一份本已空
              // 的草稿在 30 天 TTL 内一直活着。
              //
              // 旧 `interrupted` 记录仍然有小徽标，理由恰是这条没有的：
              // 它们在会话之后才被发现，用户早忘了附过什么。
              if (target) {
                if (target.getUploads().some((u) => u.clientUploadId === clientUploadId)) {
                  target.removeUpload(clientUploadId);
                  // 粘贴成文件没有磁盘副本可重新挂载，所以文本回进 composer。
                  if (pastedText !== undefined) {
                    deliverPastedTextBack(target, editorRef, pastedText);
                  }
                }
              } else {
                setLocalUploads((prev) =>
                  prev.filter((u) => u.clientUploadId !== clientUploadId),
                );
                if (pastedText !== undefined) {
                  deliverPastedTextBack(undefined, editorRef, pastedText);
                }
              }
              toast.error(t(($) => $.upload.failed, { filename: file.name, reason }));
              resolve(null);
            }
          },
        });
      });
    },
    [binding, editorRef, issueId, commentId, chatSessionId, t],
  );

  const removeUpload = useCallback(
    (clientUploadId: string) => {
      // 为「还在途时被移除」的占位符做防御性取消：它的请求已经没有目的地，
      // 别让它跑完。今天的小徽标只对失败/中断项暴露 ✕，所以只有未来的调用方
      // 移除 `uploading` 项才会触发它。以**我们的**追踪为守卫，乱传的 id 绝不
      // 会中止别人界面的上传。
      const tracked = binding ? binding.getUploads() : localUploadsRef.current;
      if (tracked.some((u) => u.clientUploadId === clientUploadId && u.status === "uploading")) {
        abortUpload(clientUploadId);
      }
      if (binding) binding.removeUpload(clientUploadId);
      else setLocalUploads((prev) => prev.filter((u) => u.clientUploadId !== clientUploadId));
    },
    [binding],
  );

  // 本地（非持久化）路径在提交时刻的事实：刻意在渲染时写的最新值 ref，
  // 因为 `isBlocked` 必须在调用时刻读值，而不是读回调捕获的那个渲染的值。
  // 持久化路径经绑定读 store。
  const localUploadsRef = useRef(localUploads);
  localUploadsRef.current = localUploads;

  // 刻意每次渲染都重建（editorGate 本身就是每渲染一个新对象）：使用者经
  // ref/props 读它，从不按身份比较。
  const gate: UploadGate = {
    uploading: editorGate.uploading || hasUploadingDraft(uploads),
    onUploadingChange: editorGate.onUploadingChange,
    isBlocked: () =>
      editorGate.isBlocked() ||
      hasUploadingDraft(binding ? binding.getUploads() : localUploadsRef.current),
  };

  return { uploads, attachments, handleUpload, removeUpload, gate };
}
