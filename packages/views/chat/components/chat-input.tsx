// 聊天输入框：项目聊天里最核心的编辑器卡片。含草稿按会话隔离持久化、附件上传
// （协调式上传引擎）、@提及上下文、项目上下文 chip、开场建议/草稿恢复接管，
// 以及"等待-再渲染"的发送契约（先等服务端接受再清空输入框）。
"use client";

import type { ReactNode } from "react";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { TriangleAlert } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  ContentEditor,
  type ContentEditorRef,
  useFileDropZone,
  FileDropOverlay,
  useUploadGate,
  useComposerSubmit,
} from "../../editor";
import { PASTE_AS_FILE_THRESHOLD } from "../../editor/paste-as-file";
import {
  useCoordinatedUploads,
  type UploadDraftBinding,
} from "../../editor/use-coordinated-uploads";
import { SubmitButton } from "@multica/ui/components/common/submit-button";
import { ChatAddMenu } from "./chat-add-menu";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useChatStore, DRAFT_NEW_SESSION } from "@multica/core/chat";
import { attachmentToDraftUpload, type DraftUpload } from "@multica/core/drafts";
import { createLogger } from "@multica/core/logger";
import { formatShortcut, useShortcut } from "@multica/core/shortcuts";
import type { MentionItem } from "../../editor/extensions/mention-suggestion";
import type { Attachment, Project } from "@multica/core/types";
import { ProjectPicker } from "../../projects/components/project-picker";
import { ClearablePillButton } from "../../common/pill-button";
import { useT } from "../../i18n";

const logger = createLogger("chat.ui");
const EMPTY_UPLOADS: DraftUpload[] = [];
/** 聊天输入框的编辑器身份——见下方 editorKey 的说明。 */
const CHAT_COMPOSER_EDITOR_KEY = "chat-composer";

function attachmentReferenceUrls(attachment: Attachment): string[] {
  const withUploadFields = attachment as Attachment & {
    markdownLink?: string;
    link?: string;
  };
  return [
    withUploadFields.markdownLink,
    attachment.markdown_url,
    attachment.download_url,
    attachment.url,
    withUploadFields.link,
    attachment.id ? `/api/attachments/${attachment.id}/download` : "",
  ].filter((url): url is string => !!url);
}

function isAttachmentReferenced(content: string, attachment: Attachment): boolean {
  return attachmentReferenceUrls(attachment).some((url) => content.includes(url));
}

interface ChatInputProps {
  onSend: (
    content: string,
    attachmentIds: string[] | undefined,
    commitInput: (options?: { extraDraftKeys?: string[]; clearEditor?: boolean }) => void,
    draftAttachments: Attachment[],
    healthReportIds?: string[],
  ) => void | boolean | Promise<void | boolean>;
  restoreDraftRequest?: {
    id: string;
    content: string;
    attachments?: Attachment[];
    /**
     * 这条恢复要落进的草稿槽位。设置后，恢复只在用户正看着那个会话时触发——
     * 一次"发完即走"的发送若之后失败，内容会恢复到它发出时的那个会话，
     * 而不是用户后来导航到的会话。省略则恢复到当前草稿（旧行为）。
     */
    sessionId?: string;
  } | null;
  /** 开场建议按钮请求的显式替换。与同步的草稿写入不同，它可以有意覆盖脏编辑器
   * 内容，所以 ChatInput 会先取消任何待执行的防抖、再采纳它。 */
  conversationStarterRequest?: {
    id: number;
    content: string;
  } | null;
  /** 在请求既替换了存储草稿也替换了实时草稿之后触发。 */
  onConversationStarterApplied?: () => void;
  /**
   * 仅当恢复的内容/附件真正被写进草稿时触发。输入框此刻还采纳不了的恢复
   * （用户正写到一半）不会被上报：它保持 pending，等草稿一清空就落地。
   * 持有持久化服务端恢复（#5219）的调用方因此可以把它当作唯一的终点转换，
   * 进而消费那行记录。
   */
  onRestoreDraftApplied?: () => void;
  /** 该输入框是否可用上传（已选中 agent）。传输本身是协调式上传引擎
   *  （MUL-5181 L2）——调用方只声明有没有这个入口。为 false 时，粘贴 / 拖拽 /
   *  按钮仍会往编辑器里敲字，但不触发任何上传。 */
  uploadEnabled?: boolean;
  onStop?: () => void;
  isRunning?: boolean;
  /** 只有服务端明确宣告支持后续排队时才允许运行中提交。 */
  allowSubmitWhileRunning?: boolean;
  disabled?: boolean;
  /** 用户没有任何可用 agent 时为 true——禁用编辑器并显示不同的占位文案。
   *  与 `disabled` 分开，避免改动归档会话的文案。 */
  noAgent?: boolean;
  /** 当 `disabled` 是因为绑定的 agent 被归档（退役）、而不是会话本身被归档时为
   *  true——更换占位文案，让只读原因读起来准确。 */
  agentArchived?: boolean;
  /** 当 `disabled` 是因为调用方已不能再 *调用* 绑定的 agent（被改成 personal、
   *  所有权转移、被移出允许列表）时为 true。与 `noAgent` 不同：agent 确实绑定了、
   *  transcript 也可读，只是跑不了它（MUL-6380）。在占位文案里优先于 `noAgent`——
   *  当工作区里唯一的 agent 就是那个被撤销的时，两者都为 true，"创建 agent"
   *  就成了错误的指引。 */
  agentAccessRevoked?: boolean;
  agentRuntimeRequired?: boolean;
  /** 当前选中 agent 的名字，用于占位文案。 */
  agentName?: string;
  /** 渲染在输入栏左下角的附加元素——通常是 agent 选择器。 */
  leftAdornment?: ReactNode;
  /** 聊天 @ 建议：当前/最近的 issue 与项目条目。 */
  contextItems?: MentionItem[];
  /** 供草稿或当前会话选择的可选项目上下文。 */
  projects?: Project[];
  projectId?: string | null;
  onProjectChange?: (projectId: string | null) => void;
  isProjectUpdating?: boolean;
  /** 活动 agent 的 daemon 是否旧到无法把项目描述注入运行简报。软信号：选择仍可用，
   *  输入框只在 chip 旁和项目子菜单里展示一条警告。 */
  projectContextUnsupported?: boolean;
  /** 所有者每次想抢回输入框键盘焦点时加一的单调 nonce——目前发生在"新建聊天"时，
   *  让用户能立刻打字。0（初始值）是惰性的，所以普通深链接打开永不抢焦点；
   *  只有显式加一才会。 */
  focusRequest?: number;
  /**
   * 内嵌聊天界面（不参与全局聊天选中 store 却共用输入框，例如 Agent Builder）
   * 的存储 / 身份隔离覆盖。
   */
  draftKeyOverride?: string;
  editorKeyOverride?: string;
}

/**
 * 聊天输入框：底部编辑器卡片。职责包括——按会话隔离并持久化草稿、协调上传引擎、
 * 开场建议与草稿恢复的接管、发送键的"等待-再渲染"契约、项目上下文 chip 等。
 * 详见 ChatInputProps 各字段说明。
 */
export function ChatInput({
  onSend,
  restoreDraftRequest,
  conversationStarterRequest,
  onConversationStarterApplied,
  onRestoreDraftApplied,
  uploadEnabled: uploadAllowed,
  onStop,
  isRunning,
  allowSubmitWhileRunning,
  disabled,
  noAgent,
  agentArchived,
  agentAccessRevoked,
  agentRuntimeRequired,
  agentName,
  leftAdornment,
  contextItems,
  projects = [],
  projectId,
  onProjectChange,
  isProjectUpdating,
  projectContextUnsupported,
  focusRequest,
  draftKeyOverride,
  editorKeyOverride,
}: ChatInputProps) {
  const { t } = useT("chat");
  const { t: tEditor } = useT("editor");
  const sendShortcut = useShortcut("send");
  const editorRef = useRef<ContentEditorRef>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  const activeSessionId = useChatStore((s) => s.activeSessionId);
  // 两个 key，各自关心完全不同的事：
  //
  // `draftKey` — zustand 存储键。把进行中的草稿按会话隔开，避免不同会话互相串文本。
  // 未创建的聊天整个工作区只用一个槽，刻意 NOT 按 agent 键：输入框就是
  // "我还没创建的那个聊天"，`selectedAgentId` 只决定第一次发送去哪（MUL-4864）。
  // 这是存储键，不是 React 身份。
  //
  // `editorKey` — ContentEditor 上的 React `key`，即编辑器身份。它对聊天输入框
  // 恒定不变，因为"你在输入什么"的切换没有任何理由扔掉你现在正在打的实例：
  //   - 切换 agent：草稿槽没变，重挂载只会丢掉草稿防抖还没持久化的最近 <100ms。
  //   - 占位文案：ContentEditor 的占位同步 effect 会实时刷新它，从不需要重挂载。
  //   - 草稿恢复（一次被取消的运行、一次失败的发送）：写进 `inputDraft`，
  //     编辑器的同步值 effect 会把它推入实时实例。不存在第二份拷贝需要漂移或复现。
  //   - 切换会话 / 懒创建：从一个全新聊天发送会把 `activeSessionId` 从 null 变成
  //     uuid，而此时某次上传的 blob 预览可能还在文档里。按会话键的编辑器会在
  //     那一刻卸载，在 file-upload.ts 换入 CDN URL 之前丢掉图片节点——用户会看着
  //     图片闪一下然后消失。稳定的身份正是让"首次上传创建会话"表现得跟之后每次
  //     上传一样的原因。
  // 内嵌界面（Agent Builder）仍会传 `editorKeyOverride` 隔离自己的输入框。
  const draftKey = draftKeyOverride ?? activeSessionId ?? DRAFT_NEW_SESSION;
  // 选基础值——空字符串兜底保持引用稳定。
  const inputDraft = useChatStore((s) => s.inputDrafts[draftKey] ?? "");
  const storeUploads = useChatStore(
    (s) => s.inputDraftAttachments[draftKey] ?? EMPTY_UPLOADS,
  );
  const setInputDraft = useChatStore((s) => s.setInputDraft);
  const setInputDraftAttachments = useChatStore((s) => s.setInputDraftAttachments);
  const clearInputDraft = useChatStore((s) => s.clearInputDraft);
  const [isEmpty, setIsEmpty] = useState(!inputDraft.trim());
  // `isEmpty` 追踪的是 LIVE 编辑器，而持久化草稿落后它一拍防抖，所以发送可用性
  // 不能只从 `inputDraft` 推导。但 `isEmpty` 也不会在输入框切换草稿槽时重新推导：
  // ChatInput 在切换会话时不会重挂载，ContentEditor 的同步值 effect 用
  // `emitUpdate: false` 把进来的草稿推进去，不触发 onUpdate。所以要同时读两个信号——
  // 一个 `isEmpty` 还没见过的草稿（恢复的或搁置的，或任何用户在别的会话里敲过的
  // 持久化草稿）仍能让按钮可用。误启用没有代价：handleSend 读实时编辑器，
  // 空内容会直接退出。
  const hasNothingToSend = isEmpty && !inputDraft.trim();
  const appliedRestoreIdRef = useRef<string | null>(null);
  const appliedConversationStarterIdRef = useRef<number | null>(null);
  const editorKey = editorKeyOverride ?? CHAT_COMPOSER_EDITOR_KEY;

  // 编辑器实例当前"握着"的文档所属的草稿。
  //
  // 正常时和 `draftKey` 相同。两者只在一次在途上传把实例钉在来源文档上时分离：
  // ContentEditor 的 Guard 0 拒绝在 `uploading` 节点上 `setContent`，因为清掉那个
  // 节点会让上传的收尾工作搁浅、文件悄悄消失。在上传落定前，输入框仍显示——也在
  // 编辑——来源草稿，所以实例产生的每一个字节都属于这个 key，而不是用户之后
  // 导航到的任何地方。
  //
  // `draftKey` 回答"选中了什么"；这个回答"加载了什么"。编辑器驱动的每一次写入
  // 都必须用后者。
  const editorDraftKeyRef = useRef(draftKey);

  // 往一个草稿槽里写文档：文本 + 清理正文不再引用的附件（删掉图片的 markdown
  // 也会连同丢掉暂存的上传）。实时 onUpdate 和下面的草稿切换冲刷共用这一个函数，
  // 一条路径永远不会按一条规则、另一条路径按另一条规则来归档文档。附件在写入时
  // 实时读取 `key` 而不是当参数传入——发生分离时，渲染出的 `draftAttachments`
  // 属于"选中的草稿"，而不是"加载中的草稿"。
  const commitDraft = useCallback(
    (key: string, markdown: string) => {
      setInputDraft(key, markdown);
      const uploads =
        useChatStore.getState().inputDraftAttachments[key] ?? EMPTY_UPLOADS;
      if (uploads.length === 0) return;
      // 只清理"已完成"且正文不再引用的上传——删掉图片的 markdown 会连同丢掉
      // 暂存的上传。占位状态（上传中 / 失败 / 中断）还没有正文引用；状态 chip
      // 是它们唯一的 UI，所以它们必须活过每一次按键。
      const referenced = uploads.filter(
        (u) => u.status !== "uploaded" || isAttachmentReferenced(markdown, u.attachment),
      );
      if (referenced.length !== uploads.length) {
        setInputDraftAttachments(key, referenced);
      }
    },
    [setInputDraft, setInputDraftAttachments],
  );
  // 提交闸门。`uploading` 在上传一开始就让 SubmitButton 失效；`isBlocked()` 在
  // handleSend 内部会被重读，覆盖那些完全绕过按钮的路径（粘贴中按 Mod+Enter、
  // 拖拽与键盘赛跑）。两者都读编辑器文档——它才是真正的上传队列；过去这里是个
  // 本地在途计数器，手动删除 pending 图片会让它卡死（MUL-4808）。
  const uploadGate = useUploadGate(editorRef);

  // `editorDraftKeyRef` 的反应式镜像，供实时编辑器注册表使用：回写只能在文档
  // 属于"正在落定的草稿"时插入这个编辑器，而仅推进 ref 不会重跑任何 effect。
  // 由下面的 adopt layout effect 连同 ref 一起更新。
  const [loadedDraftKey, setLoadedDraftKey] = useState(draftKey);

  // 针对"一个"草稿槽的 store 访问器，可为任意 key 构建：引擎在选取时刻快照
  // `resolveUploadTarget()`，所以上传会落进——并针对——编辑器当时"握着"的草稿
  // 结算，即使之后用户切换了会话。
  const makeUploadBinding = useCallback((key: string): UploadDraftBinding => ({
    registryKey: `chat:${key}`,
    getUploads: () => useChatStore.getState().inputDraftAttachments[key] ?? EMPTY_UPLOADS,
    addUpload: (u) => useChatStore.getState().addInputDraftUpload(key, u),
    settleUpload: (id, att) => useChatStore.getState().settleInputDraftUpload(key, id, att),
    failUpload: (id, err) => useChatStore.getState().failInputDraftUpload(key, id, err),
    removeUpload: (id) => useChatStore.getState().removeInputDraftUpload(key, id),
    getBody: () => useChatStore.getState().inputDrafts[key] ?? "",
    appendToBody: (md) => useChatStore.getState().appendToInputDraft(key, md),
  }), []);
  const uploadBinding = useMemo(
    () => makeUploadBinding(draftKey),
    [makeUploadBinding, draftKey],
  );
  // 协调器持有的上传（MUL-5181 L2）：能扛过窗口关闭、登出时中止、重载后丢弃。
  // `gate` 把编辑器闸门加上草稿的占位状态一起加宽，让一个重新打开、而上传仍在
  // 运行的输入框无法越过它发送。
  const {
    uploads: draftUploads,
    attachments: draftAttachments,
    handleUpload,
    gate,
  } = useCoordinatedUploads(uploadBinding, storeUploads, {}, uploadGate, editorRef, {
    resolveUploadTarget: () => makeUploadBinding(editorDraftKeyRef.current),
    liveRegistryKey: `chat:${loadedDraftKey}`,
  });

  // 把编辑器从它"握着的"草稿挪到"选中的"草稿。
  //
  // 两个陷阱让它不止是"让值同步 effect 去处理"：
  //
  // 1. 未冲刷的按键。`onUpdate` 有防抖，当一支在草稿 A 下武装好的防抖开火时，
  //    `onUpdate` 已解析到最新一次渲染的闭包——它会把 A 的文档记到 B 名下。
  //    冲刷把那几个字节拿回来，提交回它们实际输入的那个 key。
  // 2. 一次在途的上传。Guard 0 钉住文档（见 editorDraftKeyRef），所以切换此刻
  //    根本无法发生：我们让文档和它的写入都留在来源 key 上，等闸门放行后再重试
  //    （`uploadGate.uploading` 是依赖）。反过来阻塞导航更糟——用户会为了换个
  //    标签页去等一次网络往返。
  //
  // 情况 2 之后还必须强制 adopt：ContentEditor 的同步 effect 在 Guard 0 升起时
  // 已经"消费"了这次值变化（lastSyncedValueRef 在守卫之前就前进了），所以它
  // 再也不会为该值重跑，目标草稿永远不会自己加载。
  //
  // 用 useLayoutEffect 而非 useEffect，两个排序原因：被动 effect 按子先父后运行，
  // 这里的被动冲刷会落在 ContentEditor 同步 effect 已在脏编辑器上跳过之后；
  // 而 layout effect 属于 commit 的一部分，所以没有待执行的防抖计时器能抢在它前头。
  const deferredAdoptRef = useRef(false);
  useLayoutEffect(() => {
    const loadedKey = editorDraftKeyRef.current;
    if (loadedKey === draftKey) return;
    if (editorRef.current?.hasActiveUploads() === true) {
      // 被钉住了。在上传落定前保持绑定来源草稿。
      deferredAdoptRef.current = true;
      logger.debug("input.draft switch deferred by in-flight upload", {
        from: loadedKey,
        to: draftKey,
      });
      return;
    }
    const pending = editorRef.current?.flushPendingUpdate() ?? null;
    if (pending !== null) {
      logger.debug("input.draft flush on key change", { from: loadedKey, to: draftKey });
      commitDraft(loadedKey, pending);
    }
    editorDraftKeyRef.current = draftKey;
    setLoadedDraftKey(draftKey);
    if (!deferredAdoptRef.current) return;
    deferredAdoptRef.current = false;
    const incoming = useChatStore.getState().inputDrafts[draftKey] ?? "";
    logger.debug("input.draft adopting after upload settled", { key: draftKey });
    editorRef.current?.adoptContent(incoming);
    setIsEmpty(!incoming.trim());
  }, [draftKey, uploadGate.uploading, commitDraft]);

  // 所有者加一 `focusRequest` 时抢回键盘焦点（新建了一个聊天），让用户能立刻打字。
  // 编辑器的 `focus()` 在未挂载时会锁存到 `onCreate`，所以即使输入框首次渲染
  // 也能生效。`0` 刻意惰性。
  useEffect(() => {
    if (!focusRequest) return;
    editorRef.current?.focus();
  }, [focusRequest]);

  // 开场建议的点击是一次显式替换，不是普通的外部 store 同步。ContentEditor
  // 在有脏的本地文本时会刻意拒绝外部值，所以在这里 adopt 能防止旧文本待执行的
  // 防抖赢得竞态、覆盖掉选中的提示词。还要等上传结束：在有上传节点时 adopt
  // 内容会搁浅它的完成回调。
  useLayoutEffect(() => {
    if (!conversationStarterRequest) return;
    if (appliedConversationStarterIdRef.current === conversationStarterRequest.id) return;
    if (editorDraftKeyRef.current !== draftKey) return;
    if (editorRef.current?.hasActiveUploads() === true) return;

    editorRef.current?.flushPendingUpdate();
    appliedConversationStarterIdRef.current = conversationStarterRequest.id;
    commitDraft(draftKey, conversationStarterRequest.content);
    editorRef.current?.adoptContent(conversationStarterRequest.content);
    setIsEmpty(!conversationStarterRequest.content.trim());
    onConversationStarterApplied?.();
  }, [
    commitDraft,
    draftKey,
    onConversationStarterApplied,
    conversationStarterRequest,
    uploadGate.uploading,
  ]);

  useEffect(() => {
    if (!restoreDraftRequest) {
      appliedRestoreIdRef.current = null;
      return;
    }
    if (appliedRestoreIdRef.current === restoreDraftRequest.id) return;
    // 会话级恢复：如果这条草稿属于某个具体会话，就等用户真的在看他时再动。
    // 一次"发完即走"的发送在用户导航走后失败，绝不能把内容倒进用户此刻正看的
    // 会话——请求保持 pending，直到用户回到来源会话（那时 draftKey 就匹配了）。
    if (restoreDraftRequest.sessionId && restoreDraftRequest.sessionId !== draftKey) {
      return;
    }
    // 草稿里有文本或暂存附件 = 用户写到一半的活——绝不能用恢复去覆盖两者中的
    // 任何一个（下面的附件写入会整份替换暂存列表）。这是 WAIT，不是决定：
    // 请求保持 pending，这个 effect 会随每次草稿变化重跑，所以用户一发送或清掉
    // 正在输入的内容，恢复就会落地。在这里把它标记完成会让它在输入框余生里搁浅。
    if (inputDraft.trim() || draftUploads.length > 0) {
      logger.debug("input.restore waiting: draft has content", {
        draftKey,
        restoreId: restoreDraftRequest.id,
      });
      return;
    }
    appliedRestoreIdRef.current = restoreDraftRequest.id;
    setInputDraft(draftKey, restoreDraftRequest.content);
    // 恢复携带的是已完成的服务器行——把它们按"已上传"条目暂存。
    setInputDraftAttachments(
      draftKey,
      (restoreDraftRequest.attachments ?? []).map(attachmentToDraftUpload),
    );
    setIsEmpty(!restoreDraftRequest.content.trim());
    onRestoreDraftApplied?.();
  }, [
    draftKey,
    inputDraft,
    draftUploads,
    onRestoreDraftApplied,
    restoreDraftRequest,
    setInputDraft,
    setInputDraftAttachments,
  ]);

  // 拖拽区包住圆角卡片，让拖到输入面任何地方的文件都走编辑器的上传扩展
  // （与编辑器内粘贴同一条处理器）。
  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => editorRef.current?.uploadFile(f)),
  });

  // 统一的"等待-再渲染"发送契约（MUL-5181）。`useComposerSubmit` 负责六步
  // 悲观提交——空内容守卫、同步单飞（用 ref，同一 tick 里的双击 Mod+Enter
  // 不会钻过落后一拍的 state 布尔值而重复发送）、提交时重新检查上传、
  // 以及 `submitting` 锁 / 转圈。聊天特有的工作——已加载草稿守卫、附件 id 解析、
  // 由调用方驱动的 `commitInput` 交接——留在这里的 `onSubmit`，它收到的
  // 是已经归一化的内容。
  // 当 `commitInput` 真的清扫了可见文档时置位；`afterAccepted` 稍后读回它，
  // 决定光标是否可以返回。每次提交开头重置，让一次被拒的发送不会给下一次
  // 留下过期的 `true`。
  const editorScrubbedRef = useRef(false);

  const { submitting, submit } = useComposerSubmit({
    editorRef,
    uploadGate: gate,
    containerRef: composerRef,
    // 聊天在发送后把光标留在框里（下一轮还在同一个地方输入），除非 commit
    // 根本没动编辑器——见 `editorScrubbedRef`。
    afterAccepted: () => (editorScrubbedRef.current ? "refocus" : "none"),
    onSubmit: async (content: string): Promise<boolean> => {
      editorScrubbedRef.current = false;
      // 这些状态禁用了 SubmitButton，但 Mod+Enter 会绕过它——所以只读或忙碌的
      // 输入框仍必须拒绝键盘路径。
      if (disabled || noAgent || (isRunning && !allowSubmitWhileRunning)) {
        logger.debug("input.send skipped", {
          disabled,
          noAgent,
          runningWithoutQueueSupport: !!isRunning && !allowSubmitWhileRunning,
        });
        return false;
      }
      // 编辑器握着的还是*另一个*草稿的文档、而不是选中的那个——一次上传钉住了它，
      // 下面的 adopt 还没跑。上传闸门（useComposerSubmit 内部会复查）覆盖了
      // 那个窗口的大部分，但盖不住"上传最终派发（节点消失）到 adopt 的重渲染"
      // 之间的缝隙：React 会在一个调度任务里刷新那次状态更新，所以一次提交可能
      // 先到。在这里发送会把来源草稿的文本发进选中的会话，然后清错草稿。
      if (editorDraftKeyRef.current !== draftKey) {
        logger.debug("input.send skipped: composer still holds another draft", {
          loaded: editorDraftKeyRef.current,
          selected: draftKey,
        });
        return false;
      }
      // 只发送仍存在于内容里的附件 id。删掉 / 改掉 markdown URL 的同时也解除绑定。
      const activeIds: string[] = [];
      for (const attachment of draftAttachments) {
        if (isAttachmentReferenced(content, attachment)) activeIds.push(attachment.id);
      }
      const uniqueActiveIds = Array.from(new Set(activeIds));
      // 在 onSend 之前捕获草稿 key——创建新会话会同步改动 activeSessionId，
      // 在 onSend 之后再读就会指向新会话、把旧草稿晾在一边。
      const keyAtSend = draftKey;
      // 过期提交守卫（MUL-5181 P0）：快照这条已发草稿的持久化值，先冲刷编辑器
      // 待执行的防抖，让提交前的输入也进快照。一次迟到的成功只能清掉它真正
      // 发送的内容——发送*期间*敲的字（或这个输入框卸载后被重新打开敲的字）
      // 会同时活在编辑器与 store 里。
      const pendingFlush = editorRef.current?.flushPendingUpdate?.();
      if (pendingFlush != null) commitDraft(editorDraftKeyRef.current, pendingFlush);
      const draftValueAtSend = useChatStore.getState().inputDrafts[keyAtSend];
      let committed = false;
      const commitInput = (options?: { extraDraftKeys?: string[]; clearEditor?: boolean }) => {
        if (committed) return;
        committed = true;
        // `clearEditor === false` 表示调用方在用户已经导航到别的会话后"发完即走"。
        // 编辑器实例跨会话共享，所以它此刻显示着（用户可能正在敲的）一份*别的*
        // 草稿——清掉它或让它失焦会抹掉那份可见的输入。只有当用户仍在发出时的
        // 那个会话上，才去清扫编辑器。
        // 先冲刷仍躺在防抖窗口里的输入再下判断：它属于"加载中的文档"，
        // 必须算作一次中途编辑。
        const lateMd = editorRef.current?.flushPendingUpdate?.();
        if (lateMd != null) commitDraft(editorDraftKeyRef.current, lateMd);
        const liveDraft = useChatStore.getState().inputDrafts[keyAtSend];
        const untouched = liveDraft === undefined || liveDraft === draftValueAtSend;
        if (options?.clearEditor !== false && untouched) {
          editorRef.current?.clearContent();
          // 清掉了用户正看着的文档，光标就该回到这里：聊天是一来一回的对话，
          // 下一轮在同一格输入。`useComposerSubmit` 读这个标记来决定，因为其它
          // 分支绝不能抢焦点——从另一个会话"发完即走"（`clearEditor: false`）
          // 会让这个共享编辑器显示一份用户可能正在输入的*别的*草稿。
          editorScrubbedRef.current = true;
          setIsEmpty(true);
        }
        // 只有未被改动时才清掉已发草稿的数据——消息确实在路上了，但快照之后
        // 被替换的草稿是更新的活，不能作为附带损失浮现出来。
        if (untouched) clearInputDraft(keyAtSend);
        for (const key of options?.extraDraftKeys ?? []) {
          if (key !== keyAtSend) clearInputDraft(key);
        }
      };
      logger.info("input.send", {
        contentLength: content.length,
        draftKey: keyAtSend,
        attachmentCount: uniqueActiveIds.length,
      });
      const accepted = await onSend(
        content,
        uniqueActiveIds.length > 0 ? uniqueActiveIds : undefined,
        commitInput,
        draftAttachments.filter((attachment) => uniqueActiveIds.includes(attachment.id)),
      );
      // 调用方拒绝了发送（或抛错，useComposerSubmit 同样当作拒绝）：草稿从未
      // 被提交，所以它留在原地供重试。只有一次 commit——这里或调用方——会清掉它。
      if (accepted === false) return false;
      if (!committed) commitInput();
      return true;
    },
  });

  const placeholder = agentAccessRevoked
    ? t(($) => $.input.placeholder_access_revoked)
    : noAgent
    ? t(($) => $.input.placeholder_no_agent)
    : disabled
      ? agentArchived
        ? t(($) => $.input.placeholder_archived_agent)
        : agentRuntimeRequired
          ? t(($) => $.input.placeholder_runtime_required)
        : t(($) => $.input.placeholder_archived)
      : agentName
        ? t(($) => $.input.placeholder_named, { name: agentName })
        : t(($) => $.input.placeholder_default);

  const uploadEnabled = !!uploadAllowed && !disabled && !noAgent;
  // 只在发送请求本身正创建/解析目标会话时锁定。一旦被接受，agent 干活时仍可把
  // 它的项目摘掉：改会话元数据不会取消或移动那个任务。
  const projectSelectionEnabled =
    !!onProjectChange &&
    !disabled &&
    !noAgent &&
    !submitting &&
    !isProjectUpdating;
  const selectedProject = projects.find((project) => project.id === projectId);

  return (
    <div
      ref={composerRef}
      className={cn(
        // 输入框随草稿长高，最多到它所在界面的一半——固定 160px 的上限会让
        // 长草稿在一个五行小窗口里没法读（MUL-5196）。`max-h-[50%]` 相对聊天界面
        // （悬浮窗 / 聊天标签页 / agent 构建器）解析，它们都给这个容器一个确定
        // 高度，所以用户缩放 / 展开窗口时上限会跟着变。容器必须是 flex 列，
        // 下面的卡片才能缩进这个上限里、而不是溢出。
        "flex max-h-[50%] min-h-0 flex-col pb-3 pt-0",
        CHAT_GUTTER,
        // 固定的抬高，与队列无关：ChatQueue 把底边缩到这块表面下面（它那侧的
        // z-0 + 负外边距），输入框只是永远画在上面。它自己的外观从不变化。
        "relative z-10",
        // 外层容器扛"禁用光标"。内层卡片设 pointer-events-none，抑制悬停
        // （从而也抑制它自己的光标）——把两层拆开让悬停冒泡回这里，
        // 浏览器才真的读到光标。
        noAgent && "cursor-not-allowed",
      )}
    >
      <div
        data-slot="chat-input-surface"
        {...(uploadEnabled ? dropZoneProps : {})}
        className={cn(
          // max-h-96 是叠加在容器 50% 之上的绝对上限：在很高的界面上，
          // 一半高度比任何人一次能读的输入框都长，而且如果未来某个宿主在
          // 没有确定高度的地方挂载输入框（百分比 max-height 会解析成 none），
          // 它仍能保证上限有限。
          CHAT_COLUMN,
          "relative flex min-h-16 max-h-96 flex-col rounded-lg border border-surface-border bg-surface pb-9 transition-[border-color,box-shadow] focus-within:border-brand focus-within:ring-2 focus-within:ring-ring/20",
          // 没有 agent 时的视觉 + 交互锁。我们不去切换 ContentEditor 的可编辑
          // 模式（Tiptap 挂载后无法干净切换，而且该 prop 已被移除）；而是在
          // 容器层把指针事件去掉，让点击根本够不到编辑器，并压暗表面，让它
          // 读起来是"禁用"而不是"坏了"。
          noAgent && "pointer-events-none opacity-60",
        )}
        aria-disabled={noAgent || undefined}
      >
        {selectedProject && (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 pt-2">
            <div
              className={cn(
                "inline-flex max-w-full",
                !projectSelectionEnabled && "pointer-events-none opacity-60",
              )}
            >
              <ProjectPicker
                projectId={selectedProject.id}
                onUpdate={(updates) => onProjectChange?.(updates.project_id ?? null)}
                disabled={!projectSelectionEnabled}
                triggerRender={
                  <ClearablePillButton
                    disabled={!projectSelectionEnabled}
                    aria-label={t(($) => $.input.change_project_context)}
                    title={t(($) => $.input.change_project_context)}
                    onClear={() => onProjectChange?.(null)}
                    clearLabel={t(($) => $.input.remove_project_context)}
                    className="h-6 border-surface-border bg-surface-raised font-medium text-foreground"
                  />
                }
              />
            </div>
            {projectContextUnsupported && (
              <span className="inline-flex min-w-0 items-center gap-1 text-caption text-warning">
                <TriangleAlert className="size-3 shrink-0" />
                {t(($) => $.input.project_context_unsupported)}
              </span>
            )}
          </div>
        )}
        <div className="flex-1 min-h-0 overflow-y-auto px-3 py-2">
          <ContentEditor
            // 见上方 editorKey / draftKey 的拆分说明——编辑器身份刻意既不跟随
            // 会话、也不跟随 agent。
            key={editorKey}
            ref={editorRef}
            value={inputDraft}
            placeholder={placeholder}
            onUpdate={(md) => {
              setIsEmpty(!md.trim());
              // 用"加载中的 key"，而不是选中的 key：上传钉住文档期间，这里
              // 触发的是来源草稿正文的写入——包括上传自己的完成派发。
              commitDraft(editorDraftKeyRef.current, md);
            }}
            onSubmit={submit}
            onUploadFile={uploadEnabled ? handleUpload : undefined}
            pasteAsFileThreshold={PASTE_AS_FILE_THRESHOLD}
            onUploadingChange={uploadGate.onUploadingChange}
            attachments={draftAttachments}
            debounceMs={100}
            mentionMode={contextItems ? "context" : "default"}
            mentionContextItems={contextItems}
            enableSlashCommands
            // 气泡菜单带着唯一能剥掉格式的入口——"普通文本"（setParagraph）
            // 加上加粗 / 列表这些开关。一旦 `# ` 输入规则或一次 Markdown/HTML
            // 粘贴把一行变成标题，聊天就没有别的办法去掉它了，所以没有气泡
            // 菜单，格式只能造、不能撤销（MUL-5106）。
            showBubbleMenu
          />
        </div>
        {(uploadEnabled || projectSelectionEnabled || leftAdornment) && (
          <div className="absolute bottom-1.5 left-1.5 flex items-center gap-1">
            {(uploadEnabled || projectSelectionEnabled) && (
              <ChatAddMenu
                onSelectFile={uploadEnabled
                  ? (file) => editorRef.current?.uploadFile(file)
                  : undefined}
                projects={projects}
                projectId={projectId}
                onSelectProject={projectSelectionEnabled ? onProjectChange : undefined}
                projectContextUnsupported={projectContextUnsupported}
              />
            )}
            {leftAdornment}
          </div>
        )}
        <div className="absolute bottom-1 right-1.5 flex items-center gap-1">
          <SubmitButton
            onClick={submit}
            disabled={hasNothingToSend || submitting || !!disabled || !!noAgent}
            loading={submitting}
            busy={gate.uploading}
            // 支持排队的运行复用一个动作槽：输入框为空时显示 Stop，
            // 有实时内容时换成"排队发送"。旧服务端不接受后续消息，所以它们仍只有
            // Stop。上传会阻塞提交，因此此时也回退到 Stop，而不是删掉聊天唯一的
            // 取消路径；附件节点仍是编辑器里可见的上传进度表面。
            running={
              !!isRunning &&
              (!allowSubmitWhileRunning || hasNothingToSend || gate.uploading)
            }
            onStop={onStop}
            tooltip={gate.uploading
              ? tEditor(($) => $.upload.in_progress)
              : isRunning
                ? t(($) => $.input.queue_send_tooltip)
                : sendShortcut
                  ? `${t(($) => $.input.send_tooltip)} · ${formatShortcut(sendShortcut)}`
                  : t(($) => $.input.send_tooltip)}
            ariaLabel={gate.uploading
              ? tEditor(($) => $.upload.in_progress)
              : isRunning
                ? t(($) => $.input.queue_send_tooltip)
              : t(($) => $.input.send_tooltip)}
            stopTooltip={t(($) => $.input.stop_tooltip)}
            stopAriaLabel={t(($) => $.input.stop_tooltip)}
          />
        </div>
        {uploadEnabled && isDragOver && <FileDropOverlay />}
      </div>
    </div>
  );
}
