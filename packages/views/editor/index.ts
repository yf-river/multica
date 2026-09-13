/**
 * editor 包对外出口（barrel）。
 *
 * 统一导出编辑器家族：ContentEditor（富文本主体）、TitleEditor（标题）、
 * ReadonlyContent（只读渲染），以及附件下载/预览/图片序列、上传门禁、
 * 协调上传、lazy 编辑、文本锚点等配套 hook 与组件。
 */
export {
  ContentEditor,
  type ContentEditorProps,
  type ContentEditorRef,
} from "./content-editor";
export {
  TitleEditor,
  type TitleEditorProps,
  type TitleEditorRef,
} from "./title-editor";
export { ReadonlyContent } from "./readonly-content";
export { useFileDropZone } from "./use-file-drop-zone";
export { useUploadGate, type UploadGate } from "./use-upload-gate";
export {
  useComposerSubmit,
  type ComposerSubmit,
  type ComposerSubmitOptions,
  type ComposerAfterAccepted,
} from "./use-composer-submit";
export { useEditorUpload } from "./use-editor-upload";
export { FileDropOverlay } from "./file-drop-overlay";
export { useLazyEditor, type LazyEditorHandle, type LazyFocusTarget } from "./use-lazy-editor";
export { anchorFromPoint, type TextAnchor } from "./text-anchor";
export { useDownloadAttachment } from "./use-download-attachment";
export { AttachmentDownloadProvider } from "./attachment-download-context";
export {
  AttachmentPreviewModal,
  useAttachmentPreview,
  isPreviewable,
} from "./attachment-preview-modal";
export type {
  AttachmentPreviewHandle,
  PreviewSequence,
} from "./attachment-preview-modal";
export {
  ImageSequenceProvider,
  useImageSequencePreview,
} from "./image-sequence-context";
export { AttachmentCard } from "./attachment-card";
export type { AttachmentCardProps } from "./attachment-card";
export { Attachment } from "./attachment";
export type { AttachmentInput, AttachmentProps } from "./attachment";
