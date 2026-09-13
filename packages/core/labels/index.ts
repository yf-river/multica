// 本文件是 labels（标签）模块的对外出口（barrel）。

export { labelKeys, labelListOptions, issueLabelsOptions, resourceLabelsOptions } from "./queries";
export {
  useCreateLabel,
  useUpdateLabel,
  useDeleteLabel,
  useAttachLabel,
  useAttachLabelToIssue,
  useDetachLabel,
  useAttachResourceLabel,
  useDetachResourceLabel,
} from "./mutations";
