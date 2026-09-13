"use client";

/**
 * 接受附件的 composer 的「提交门禁」（MUL-4808）。
 *
 * 文件仍在上传时，编辑器里挂着一个 `blob:` 占位符，附件的 id 还不存在。
 * 那一刻序列化对占位符什么都输出不了（见 image / fileCard 的
 * `renderMarkdown`），也绑不到任何 id——发送会「成功」，而文件从收件人拿到的
 * 内容里悄然消失。每个会**落定**草稿的动作——发送、创建、保存，以及把草稿
 * 重新序列化成另一种形态的模式切换——都必须等上传完成。
 *
 * 两条信号，因为它们回答的是不同的问题：
 *
 *   `uploading`——响应式，驱动渲染出来的按钮（禁用 / "上传中…" / aria-busy）。
 *   数据源是编辑器文档经 `onUploadingChange` 上报的实际上传队列。
 *
 *   `isBlocked()`——命令式，在提交处理器**内部**读取。渲染出的禁用态永远
 *   慢一帧：Cmd+Enter 和标题栏 Enter 完全绕过按钮，一次点击也能落进上传刚
 *   开始的那一帧。它调用时刻读文档，是唯一不可能过期的答案。
 *
 * 宿主必须两者都用。只挡按钮，快捷键路径是开的；只挡处理器，会留下一个
 * 看着能用、点了却没反应的按钮。
 *
 * 刻意不 import `api` / i18n / toast——失败 toast 在 `use-editor-upload`
 * 里——让宿主测试可以在不把 API 单例拉进模块图的情况下用真实门禁对 mock
 * 编辑器跑测试。
 */

import { useCallback, useState, type RefObject } from "react";
import type { ContentEditorRef } from "./content-editor";

interface UploadGate {
  /** 本编辑器内任一带附件是否正在上传中。渲染状态。 */
  uploading: boolean;
  /** 传给 `<ContentEditor onUploadingChange={...} />`。 */
  onUploadingChange: (uploading: boolean) => void;
  /** 提交时刻的事实。在每个 submit/save/switch 处理器开头调用。 */
  isBlocked: () => boolean;
}

/**
 * 单个编辑器的「响应式 + 提交时刻」上传状态。
 *
 * 返回的 `isBlocked` 读的是编辑器 ref 而不是 `uploading`——见本模块头部
 * 文档。
 */
function useUploadGate(editorRef: RefObject<ContentEditorRef | null>): UploadGate {
  const [uploading, setUploading] = useState(false);
  const isBlocked = useCallback(
    () => editorRef.current?.hasActiveUploads() === true,
    [editorRef],
  );
  return { uploading, onUploadingChange: setUploading, isBlocked };
}

export { useUploadGate, type UploadGate };
