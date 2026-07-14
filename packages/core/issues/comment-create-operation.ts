import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import type { Comment, CreateCommentRequest } from "../types";
import { generateUUID } from "../utils";
import {
  type PendingCommentCreate,
  useCommentDraftStore,
} from "./stores/comment-draft-store";

type CommentCreateClient = Pick<ApiClient, "createComment">;

const operationScope = (issueId: string, request: CreateCommentRequest) =>
  `${issueId}:${request.parent_id ?? "root"}`;

async function executeCommentCreate(
  client: CommentCreateClient,
  scope: string,
  operation: PendingCommentCreate,
): Promise<Comment> {
  return executeRecoverableMutation(
    () => client.createComment(operation.issueId, operation.request, operation.requestKey),
    () => useCommentDraftStore.getState().setPendingCreate(scope),
  );
}

export async function createCommentWithRecovery(
  issueId: string,
  request: CreateCommentRequest,
  client: CommentCreateClient = api,
): Promise<Comment> {
  const scope = operationScope(issueId, request);
  const pending = useCommentDraftStore.getState().pendingCreates[scope];
  if (pending) {
    const recovered = await executeCommentCreate(client, scope, pending);
    if (JSON.stringify(pending.request) === JSON.stringify(request)) return recovered;
  }
  const operation = { issueId, request, requestKey: generateUUID(), createdAt: Date.now() };
  useCommentDraftStore.getState().setPendingCreate(scope, operation);
  return executeCommentCreate(client, scope, operation);
}
