// 本文件是 attachments 模块的对外出口（barrel），统一再导出图片序列相关能力。

export {
  collectImageSequence,
  indexOfImageKey,
  isImageAttachment,
  matchAttachmentByURL,
  selectStandaloneAttachments,
  type ImageSequenceBlock,
  type ImageSequenceItem,
} from "./image-sequence";
