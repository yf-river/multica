import { describe, expect, it, vi, afterEach } from "vitest";
import { fireEvent, render, screen, cleanup } from "@testing-library/react";
import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const APP_ORIGIN = "https://app.example";

vi.mock("../../issues/hooks", () => ({
  useResolveIssueIdentifier: () => null,
}));

// Only the workspace hooks are stubbed — the real path helpers stay in place so
// the reserved-slug rule that decides in-app vs external is the shipped one.
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/test/issues/${id}`,
    projectDetail: (id: string) => `/test/projects/${id}`,
  }),
  useWorkspaceSlug: () => "test",
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({ push: vi.fn() }),
  useOptionalNavigation: () => ({ push: vi.fn() }),
  resolveClickIntent: () => "push",
  useAppOrigin: () => APP_ORIGIN,
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

vi.mock("../../editor/link-hover-card", () => ({
  useLinkHover: () => ({}),
  LinkHoverCard: () => null,
}));

vi.mock("mermaid", () => ({
  default: { initialize: vi.fn(), render: vi.fn() },
}));


vi.mock("../../i18n", () => ({ useT: () => ({ t: () => "Empty" }) }));
import { FileViewer } from "./file-viewer";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function renderViewer() {
  const files: Record<string, string> = {
    "SKILL.md": "| Material |\n| --- |\n| [Project](references/project-map.md) |",
    "references/project-map.md": "# Project map\n[Sibling](./workflow.md)\n[Home](../SKILL.md)\n[External](https://example.org/docs)",
    "references/workflow.md": "# Workflow\n[Home](../SKILL.md)",
  };
  function Viewer() {
    const [path, setPath] = useState("SKILL.md");
    return <><output>{path}</output><FileViewer path={path} content={files[path]!}
      mode="preview" readOnly filePaths={Object.keys(files)} onSelectPath={setPath} onChange={vi.fn()} /></>;
  }
  return render(<QueryClientProvider client={new QueryClient()}><Viewer /></QueryClientProvider>);
}

describe("skill document links", () => {
  it("opens linked supporting files and resolves links relative to the selected file", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    renderViewer();
    fireEvent.click(screen.getByRole("link", { name: "Project" }));
    expect(screen.getByRole("status")).toHaveTextContent("references/project-map.md");
    expect(screen.getByRole("heading", { name: "Project map" })).toBeVisible();
    fireEvent.click(screen.getByRole("link", { name: "Sibling" }));
    expect(screen.getByRole("heading", { name: "Workflow" })).toBeVisible();
    fireEvent.click(screen.getByRole("link", { name: "Home" }));
    expect(screen.getByRole("status")).toHaveTextContent("SKILL.md");
    expect(open).not.toHaveBeenCalled();
  });

  it("preserves the browser behavior for external links", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    renderViewer();
    fireEvent.click(screen.getByRole("link", { name: "Project" }));
    fireEvent.click(screen.getByRole("link", { name: "External" }));
    expect(open).toHaveBeenCalledWith("https://example.org/docs", "_blank", "noopener,noreferrer");
  });
});
