"use client";

import { useState, useCallback } from "react";
import type { ApiClient } from "../api/client";
import type { Attachment } from "../types";
import { MAX_FILE_SIZE } from "../constants/upload";

// Carries the full Attachment so editors that need preview metadata
// (`content_type`, `download_url`) get it directly. Two URL fields are
// surfaced because they have different lifetimes:
//
//   `link`         — the same value as `att.url`. Short-lived for the
//                    LocalStorage backend (HMAC-signed `/uploads/<key>`)
//                    and a long-lived CDN URL on S3 / CloudFront. This
//                    is what avatar / logo callers persist into
//                    `avatar_url` style fields, and what URL-only
//                    consumers (Markdown renderers without a record
//                    in hand) get to load directly. Keeping it
//                    semantically equal to `att.url` preserves the
//                    pre-MUL-3130 contract for non-markdown callers
//                    so avatar uploads do not get rerouted through
//                    the workspace-membership-gated download endpoint.
//
//   `markdownLink` — the URL the editor writes into markdown bodies.
//                    Source: `att.markdown_url` from the server, which
//                    `buildMarkdownURL` picks per deployment policy
//                    (public CDN durable URL, or
//                    `<MULTICA_PUBLIC_URL>/api/attachments/<id>/download`).
//                    The contract is "absolute, no TTL, loads natively
//                    on every client" — that's what fixes the Desktop /
//                    mobile-webview regression where a site-relative
//                    `/api/attachments/<id>/download` couldn't resolve
//                    against `file://` (MUL-3192).
//
// MUL-3130 introduced the persisted-image regression by collapsing
// these two semantics into a single `link` field; MUL-3192 followed up
// by moving the durable-URL choice from the client to the server so
// the persisted shape is correct for the deployment without the client
// having to know whether it's running on web / desktop / mobile.
export type UploadResult = Attachment & {
  link: string;
  markdownLink: string;
};

export interface UploadContext {
  issueId?: string;
  commentId?: string;
  chatSessionId?: string;
}

export function useFileUpload(
  api: ApiClient,
  onError?: (error: Error) => void,
) {
  const [uploading, setUploading] = useState(false);

  const upload = useCallback(
    async (file: File, ctx?: UploadContext): Promise<UploadResult | null> => {
      if (file.size > MAX_FILE_SIZE) {
        throw new Error("File exceeds 100 MB limit");
      }

      setUploading(true);
      try {
        const att: Attachment = await api.uploadFile(file, {
          issueId: ctx?.issueId,
          commentId: ctx?.commentId,
          chatSessionId: ctx?.chatSessionId,
        });
        return { ...att, link: att.url, markdownLink: att.markdown_url };
      } finally {
        setUploading(false);
      }
    },
    [api],
  );

  const uploadWithToast = useCallback(
    async (file: File, ctx?: UploadContext): Promise<UploadResult | null> => {
      try {
        return await upload(file, ctx);
      } catch (err) {
        onError?.(err instanceof Error ? err : new Error("Upload failed"));
        return null;
      }
    },
    [upload, onError],
  );

  return { upload, uploadWithToast, uploading };
}
