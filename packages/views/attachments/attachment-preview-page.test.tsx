import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { AttachmentPreviewPage } from "./attachment-preview-page";

const htmlText = "<html><body><h1>Report</h1></body></html>";

vi.mock("../editor/hooks/use-attachment-html-text", () => ({
  useAttachmentHtmlText: () => ({
    data: { text: htmlText },
    isLoading: false,
    error: null,
  }),
}));

vi.mock("../i18n", () => ({
  useT: () => (fn: (d: Record<string, unknown>) => string) =>
    fn({ attachment: { preview_loading: "l", preview_failed: "f" } }),
}));

describe("AttachmentPreviewPage", () => {
  it("renders the fetched HTML in a sandboxed iframe", () => {
    const { container } = render(<AttachmentPreviewPage attachmentId="att-1" />);
    const iframe = container.querySelector("iframe")!;
    expect(iframe).toBeTruthy();
    expect(iframe.getAttribute("sandbox")).toBe("allow-scripts");
    expect(iframe.getAttribute("srcdoc")).toContain("<h1>Report</h1>");
    expect(iframe.getAttribute("srcdoc")).toContain("scrollIntoView");
  });
});
