import { api, type ApiClient } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
import type { Comment, CreateCommentRequest } from "../types";
import { generateUUID } from "../utils";
import { useCommentDraftStore } from "./stores/comment-draft-store";

type CommentCreateClient = Pick<ApiClient, "createComment">;

const operationScope = (issueId: string, request: CreateCommentRequest) =>
  `${issueId}:${request.parent_id ?? "root"}`;

export async function createCommentWithRecovery(
  issueId: string,
  request: CreateCommentRequest,
  client: CommentCreateClient = api,
): Promise<Comment> {
  const scope = operationScope(issueId, request);
  const pending = useCommentDraftStore.getState().pendingCreates[scope];
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({ issueId, request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => useCommentDraftStore.getState().setPendingCreate(scope, operation),
    (operation) => client.createComment(operation.issueId, operation.request, operation.requestKey),
  );
}
