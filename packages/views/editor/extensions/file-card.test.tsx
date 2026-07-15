import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import type { ComponentType, ReactElement } from "react";
import { renderWithI18n } from "../../test/i18n";
import { withTestQueryClient } from "../../test/query";

// Tiptap NodeView primitives can't be instantiated without a full editor.
// Stub the wrapper so FileCardView renders as a plain React component and
// the DOM can be inspected directly.
const nodeViewState = vi.hoisted(() => ({
  component: null as ComponentType<any> | null,
}));

vi.mock("@tiptap/react", () => ({
  NodeViewWrapper: ({ children, ...rest }: any) => <div {...rest}>{children}</div>,
  ReactNodeViewRenderer: (component: ComponentType<any>) => {
    nodeViewState.component = component;
    return vi.fn();
  },
}));

const { getAttachmentTextContentMock, resolveAttachmentMock, openByUrlMock, tryOpenMock } =
  vi.hoisted(() => ({
    getAttachmentTextContentMock: vi.fn(),
    resolveAttachmentMock: vi.fn(),
    openByUrlMock: vi.fn(),
    tryOpenMock: vi.fn(),
  }));

vi.mock("@multica/core/api", () => ({
  api: { getAttachmentTextContent: getAttachmentTextContentMock },
  PreviewTooLargeError: class extends Error {},
  PreviewUnsupportedError: class extends Error {},
}));

vi.mock("../attachment-download-context", () => ({
  useAttachmentDownloadResolver: () => ({
    openByUrl: openByUrlMock,
    resolveAttachment: resolveAttachmentMock,
  }),
}));

vi.mock("../attachment-preview-modal", () => ({
  useAttachmentPreview: () => ({ tryOpen: tryOpenMock, open: vi.fn(), modal: null }),
}));

// HtmlAttachmentPreview (the kind="html" route through AttachmentBlock) now
// reads useNavigation() + useWorkspaceSlug() for its Open-in-new-tab button.
// Provide minimal mocks so the component renders without a real provider.
vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    openInNewTab: vi.fn(),
    getShareableUrl: (p: string) => `https://app.example${p}`,
  }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspaceSlug: () => "acme",
  };
});

vi.mock("../i18n", () => ({
  useT: () => ({
    t: (sel: (s: Record<string, Record<string, string>>) => string) =>
      sel({
        image: { download: "Download" },
        attachment: {
          preview: "Preview",
          preview_loading: "Loading preview…",
          preview_failed: "Couldn't load preview",
          open_in_new_tab: "Open in new tab",
        },
        code_block: { copy_code: "Copy code" },
        file_card: { uploading: "Uploading {{filename}}" },
      }),
  }),
}));

import { FileCardExtension } from "./file-card";

function renderWithQuery(ui: ReactElement) {
  return renderWithI18n(withTestQueryClient(ui));
}

function renderFileCard(node: any) {
  if (!nodeViewState.component) {
    FileCardExtension.config.addNodeView?.call({} as never);
  }
  const FileCardView = nodeViewState.component!;
  return renderWithQuery(<FileCardView node={node} />);
}

beforeEach(() => vi.clearAllMocks());
afterEach(() => vi.restoreAllMocks());

describe("FileCardView — HTML attachment routes through AttachmentBlock to iframe", () => {
  // Regression pin for file-card.tsx:59. The NodeView must render through
  // <AttachmentBlock>, not the older <AttachmentCard>. If someone reverts that
  // line, the dispatcher's html+attachmentId branch is bypassed and the user
  // is left with the file-card chrome — exactly the bug MUL-2330 surfaced.
  it("renders an iframe (no file-card chrome) when the node resolves to an HTML attachment", async () => {
    resolveAttachmentMock.mockReturnValue({
      id: "att-1",
      content_type: "text/html",
      url: "/uploads/report.html",
      filename: "report.html",
    });
    getAttachmentTextContentMock.mockResolvedValueOnce({
      text: "<p>chart</p>",
      originalContentType: "text/html",
    });

    const node = {
      attrs: {
        href: "/uploads/report.html",
        filename: "report.html",
        uploading: false,
      },
    } as any;

    renderFileCard(node);

    const frame = await waitFor(() => {
      const f = document.querySelector("iframe") as HTMLIFrameElement | null;
      expect(f).toBeTruthy();
      return f!;
    });
    expect(frame.getAttribute("sandbox")).toBe("allow-scripts");
    expect(frame.getAttribute("srcdoc")).toContain("<p>chart</p>");
    // The AttachmentCard chrome surfaces the filename as text inside its row.
    // HtmlAttachmentPreview replaces the chrome entirely, so the filename
    // must not appear as visible text.
    expect(screen.queryByText("report.html")).toBeNull();
  });
});
