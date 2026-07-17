/**
 * @vitest-environment jsdom
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { ApiClient, getApi, setApiInstance } from "../api";
import type { Attachment } from "../types";
import { useFileUpload } from "./use-file-upload";

function makeAttachment(overrides: Partial<Attachment> = {}): Attachment {
  return {
    id: "att-1",
    filename: "shot.png",
    url: "/uploads/ws-1/shot.png",
    download_url: "/api/attachments/att-1/download",
    markdown_url: "https://api.multica.test/api/attachments/att-1/download",
    content_type: "image/png",
    size_bytes: 1,
    ...overrides,
  };
}

function mockUpload(att: Attachment) {
  return vi.spyOn(getApi(), "uploadFile").mockResolvedValue(att);
}

async function runUpload(): Promise<Attachment | null> {
  const { result } = renderHook(() => useFileUpload());
  let upload: Attachment | null = null;
  await act(async () => {
    upload = await result.current.upload(
      new File(["data"], "shot.png", { type: "image/png" }),
    );
  });
  return upload;
}

describe("useFileUpload", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
  });

  it("returns the canonical attachment response without URL aliases", async () => {
    const att = makeAttachment({
      markdown_url: "https://cdn.multica.test/uploads/abc.png",
    });
    mockUpload(att);
    const upload = await runUpload();
    expect(upload).toEqual(att);
  });

  it("rejects oversize files before hitting the network", async () => {
    const att = makeAttachment();
    const uploadFile = mockUpload(att);
    const huge = new File([new ArrayBuffer(1)], "big.bin", {
      type: "application/octet-stream",
    });
    Object.defineProperty(huge, "size", { value: 200 * 1024 * 1024 });

    const { result } = renderHook(() => useFileUpload());
    await expect(
      act(async () => {
        await result.current.upload(huge);
      }),
    ).rejects.toThrow(/100 MB/);
    expect(uploadFile).not.toHaveBeenCalled();
  });
});
