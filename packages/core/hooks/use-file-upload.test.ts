/**
 * @vitest-environment jsdom
 */
import { describe, expect, it, vi } from "vitest";
import { renderHook, act } from "@testing-library/react";
import type { ApiClient } from "../api/client";
import type { Attachment } from "../types";
import { useFileUpload, type UploadResult } from "./use-file-upload";

// MUL-3192 — verifies that markdown persistence uses the server's durable URL.

function makeAttachment(overrides: Partial<Attachment> = {}): Attachment {
  return {
    id: "att-1",
    workspace_id: "ws-1",
    issue_id: null,
    comment_id: null,
    chat_session_id: null,
    chat_message_id: null,
    uploader_type: "member",
    uploader_id: "u-1",
    filename: "shot.png",
    url: "/uploads/ws-1/shot.png",
    download_url: "/api/attachments/att-1/download",
    markdown_url: "https://api.multica.test/api/attachments/att-1/download",
    content_type: "image/png",
    size_bytes: 1,
    created_at: "2026-06-10T00:00:00Z",
    ...overrides,
  };
}

function makeApi(att: Attachment): ApiClient {
  return {
    uploadFile: vi.fn().mockResolvedValue(att),
  } as unknown as ApiClient;
}

async function runUpload(api: ApiClient): Promise<UploadResult | null> {
  const { result } = renderHook(() => useFileUpload(api));
  let upload: UploadResult | null = null;
  await act(async () => {
    upload = await result.current.upload(
      new File(["data"], "shot.png", { type: "image/png" }),
    );
  });
  return upload;
}

describe("useFileUpload", () => {
  it("uses att.markdown_url for markdown and the storage URL for avatars", async () => {
    const att = makeAttachment({
      markdown_url: "https://cdn.multica.test/uploads/abc.png",
    });
    const upload = await runUpload(makeApi(att));
    expect(upload?.markdownLink).toBe("https://cdn.multica.test/uploads/abc.png");
    // Avatar and logo fields intentionally use the storage URL rather than
    // the authenticated attachment download endpoint.
    expect(upload?.link).toBe(att.url);
  });

  it("rejects oversize files before hitting the network", async () => {
    const att = makeAttachment();
    const api = makeApi(att);
    const huge = new File([new ArrayBuffer(1)], "big.bin", {
      type: "application/octet-stream",
    });
    Object.defineProperty(huge, "size", { value: 200 * 1024 * 1024 });

    const { result } = renderHook(() => useFileUpload(api));
    await expect(
      act(async () => {
        await result.current.upload(huge);
      }),
    ).rejects.toThrow(/100 MB/);
    expect(api.uploadFile as ReturnType<typeof vi.fn>).not.toHaveBeenCalled();
  });
});
