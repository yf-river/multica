import { useMutation } from "@tanstack/react-query";
import { api } from "../api";

export interface CreateFeedbackInput {
  message: string;
  kind: "bug" | "feature" | "general" | "praise";
  url?: string;
  workspace_id?: string;
}

export function useCreateFeedback() {
  return useMutation({
    mutationFn: (input: CreateFeedbackInput) => api.createFeedback(input),
  });
}
