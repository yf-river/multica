"use client";

import { useState, useCallback } from "react";
import { api } from "../api";
import type { Attachment } from "../types";

const MAX_FILE_SIZE = 100 * 1024 * 1024;

interface UploadContext {
  issueId?: string;
  commentId?: string;
  chatSessionId?: string;
}

export function useFileUpload() {
  const [uploading, setUploading] = useState(false);

  const upload = useCallback(
    async (file: File, ctx?: UploadContext): Promise<Attachment | null> => {
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
        return att;
      } finally {
        setUploading(false);
      }
    },
    [],
  );

  const uploadWithToast = useCallback(
    async (file: File, ctx?: UploadContext): Promise<Attachment | null> => {
      try {
        return await upload(file, ctx);
      } catch {
        return null;
      }
    },
    [upload],
  );

  return { upload, uploadWithToast, uploading };
}
