// 本文件封装「提交用户反馈」的 React Query 变更。

import { useMutation } from "@tanstack/react-query";
import { api } from "../api";
import type { FeedbackKind } from "./types";

/** 提交反馈所需的输入：反馈正文，可选携带所在 URL、工作区与反馈类型。 */
export interface CreateFeedbackInput {
  message: string;
  url?: string;
  workspace_id?: string;
  kind?: FeedbackKind;
}

/** 提交一条用户反馈到服务端。 */
export function useCreateFeedback() {
  return useMutation({
    mutationFn: (input: CreateFeedbackInput) => api.createFeedback(input),
  });
}
