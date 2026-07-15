import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

// Hoisted mock for the API singleton: vi.mock factories cannot reference
// outside-of-scope vars, but vi.hoisted runs before the import graph.
const getAttachmentMock = vi.hoisted(() => vi.fn());
const getBaseUrlMock = vi.hoisted(() => vi.fn(() => ""));
const useWorkspaceSlugMock = vi.hoisted(() =>
  vi.fn<() => string | null>(() => "acme"),
);

vi.mock("@multica/core/api", () => ({
  api: { getAttachment: getAttachmentMock, getBaseUrl: getBaseUrlMock },
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspaceSlug: useWorkspaceSlugMock,
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("../i18n", () => ({
  useT: () => ({ t: (sel: (s: { attachment: { download_failed: string } }) => string) => sel({ attachment: { download_failed: "Couldn't fetch a download link. Try again in a moment." } }) }),
}));

import { useDownloadAttachment } from "./use-download-attachment";
import { toast } from "sonner";

const SIGNED_URL =
  "https://static.example.test/file.md?Policy=p&Signature=s&Key-Pair-Id=k";

const DEFAULT_ATTACHMENT = {
  id: "att-1",
  url: "https://static.example.test/file.md",
  download_url: SIGNED_URL,
  filename: "file.md",
};

function mockAttachment(overrides: Partial<typeof DEFAULT_ATTACHMENT> = {}) {
  getAttachmentMock.mockResolvedValueOnce({
    ...DEFAULT_ATTACHMENT,
    ...overrides,
  });
}

function installDesktopBridge() {
  const downloadURL = vi.fn();
  (window as unknown as { desktopAPI: { downloadURL: typeof downloadURL } }).desktopAPI = {
    downloadURL,
  };
  return downloadURL;
}

async function runDownload(id = "att-1") {
  const appendSpy = vi.spyOn(document.body, "appendChild");
  const { result } = renderHook(() => useDownloadAttachment());
  await act(async () => {
    await result.current(id);
  });
  const anchor = appendSpy.mock.calls
    .map(([node]) => node)
    .find((node): node is HTMLAnchorElement => node instanceof HTMLAnchorElement);
  return anchor;
}

beforeEach(() => {
  vi.clearAllMocks();
  // Default: web with same-origin API (empty base). Each test that needs
  // a non-empty base (desktop standalone, server-relative download URL)
  // overrides via getBaseUrlMock.mockReturnValue(...).
  getBaseUrlMock.mockReturnValue("");
  useWorkspaceSlugMock.mockReturnValue("acme");
});

afterEach(() => {
  vi.restoreAllMocks();
  // Scrub the desktop bridge between tests so suites don't leak state.
  delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
});

describe("useDownloadAttachment (web)", () => {
  it("clicks the unified download endpoint without opening a blank tab or buffering a Blob", async () => {
    mockAttachment();
    const openSpy = vi.spyOn(window, "open");
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
    const anchor = await runDownload();

    expect(getAttachmentMock).toHaveBeenCalledWith("att-1");
    expect(openSpy).not.toHaveBeenCalled();
    expect(clickSpy).toHaveBeenCalledOnce();
    expect(anchor).toBeDefined();
    expect(anchor!.getAttribute("href")).toBe(
      "/api/attachments/att-1/download?workspace_slug=acme",
    );
    expect(anchor!.href).toBe(
      "http://localhost:3000/api/attachments/att-1/download?workspace_slug=acme",
    );
    // Empty download attribute intentionally defers the final filename to the
    // endpoint / redirected object Content-Disposition header.
    expect(anchor!.getAttribute("download")).toBe("");
    expect(anchor!.isConnected).toBe(false);
  });

  it("resolves the unified download endpoint against a configured API base", async () => {
    getBaseUrlMock.mockReturnValue("https://api.example.test/");
    mockAttachment({
      id: "att 1/slash",
    });
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
    const anchor = await runDownload("att 1/slash");

    expect(clickSpy).toHaveBeenCalledOnce();
    expect(anchor).toBeDefined();
    expect(anchor!.href).toBe(
      "https://api.example.test/api/attachments/att%201%2Fslash/download?workspace_slug=acme",
    );
  });

  it("encodes the workspace slug into the bare navigation URL instead of relying on custom headers", async () => {
    useWorkspaceSlugMock.mockReturnValueOnce("team/space");
    mockAttachment();
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
    const anchor = await runDownload();

    expect(clickSpy).toHaveBeenCalledOnce();
    expect(anchor).toBeDefined();
    expect(anchor!.getAttribute("href")).toBe(
      "/api/attachments/att-1/download?workspace_slug=team%2Fspace",
    );
  });

  it("shows a toast and does not click a download link when the workspace slug is missing", async () => {
    useWorkspaceSlugMock.mockReturnValueOnce(null);
    mockAttachment();
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
    await runDownload();

    expect(clickSpy).not.toHaveBeenCalled();
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });

  it("shows a toast and does not click a download link when the metadata preflight fails", async () => {
    getAttachmentMock.mockRejectedValueOnce(new Error("boom"));
    const openSpy = vi.spyOn(window, "open");
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
    await runDownload();

    expect(openSpy).not.toHaveBeenCalled();
    expect(clickSpy).not.toHaveBeenCalled();
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });
});

describe("useDownloadAttachment (desktop)", () => {
  it("skips the placeholder tab and hands the signed URL to the desktop download bridge", async () => {
    const downloadURL = installDesktopBridge();
    mockAttachment();
    const openSpy = vi.spyOn(window, "open");
    await runDownload();

    // No placeholder — Electron's setWindowOpenHandler would reject
    // about:blank, so we go straight to the platform's IPC bridge.
    expect(openSpy).not.toHaveBeenCalled();
    expect(downloadURL).toHaveBeenCalledWith(SIGNED_URL);
  });

  it("shows a toast when the API rejects on desktop", async () => {
    const downloadURL = installDesktopBridge();
    getAttachmentMock.mockRejectedValueOnce(new Error("network failure"));
    await runDownload();

    expect(downloadURL).not.toHaveBeenCalled();
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });

  // MUL-2976: when the backend has no CloudFront signer, `getAttachment`
  // returns a server-relative `download_url` like `/api/attachments/.../download`.
  // The Electron main-process `downloadURLSafely` requires a parsable
  // http(s) URL or it drops the request — so the renderer must resolve
  // the path against the configured API base before crossing the bridge.
  it("resolves a server-relative download_url against the API base before handing it to the desktop bridge", async () => {
    const downloadURL = installDesktopBridge();
    getBaseUrlMock.mockReturnValue("https://api.example.test");
    mockAttachment({
      download_url: "/api/attachments/att-1/download",
    });
    await runDownload();

    expect(downloadURL).toHaveBeenCalledWith(
      "https://api.example.test/api/attachments/att-1/download",
    );
  });

  it("trims a trailing slash on the API base when resolving a relative download_url", async () => {
    const downloadURL = installDesktopBridge();
    getBaseUrlMock.mockReturnValue("https://api.example.test/");
    mockAttachment({
      url: "/api/attachments/att-1/content",
      download_url: "/api/attachments/att-1/download",
    });
    await runDownload();

    expect(downloadURL).toHaveBeenCalledWith(
      "https://api.example.test/api/attachments/att-1/download",
    );
  });

  it("passes an already-absolute download_url through unchanged when the bridge is present", async () => {
    const downloadURL = installDesktopBridge();
    // Even with a non-empty base configured, a CloudFront signed URL
    // must not be re-prefixed.
    getBaseUrlMock.mockReturnValue("https://api.example.test");
    mockAttachment({
      url: "https://cdn.example.test/att-1.bin",
    });
    await runDownload();

    expect(downloadURL).toHaveBeenCalledWith(SIGNED_URL);
  });
});
