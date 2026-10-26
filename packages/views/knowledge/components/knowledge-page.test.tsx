import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import workspace from "../../locales/zh-Hans/workspace.json";
import { KnowledgePage } from "./knowledge-page";

const state = vi.hoisted(() => ({
  role: "owner",
  wsId: "ws",
  remove: vi.fn(),
  importURL: vi.fn(),
}));

vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => state.wsId }));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: state.role }),
}));
vi.mock("../../layout/page-header", () => ({
  PageHeader: ({ children }: { children: React.ReactNode }) => (
    <header>{children}</header>
  ),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@multica/core/knowledge", async (load) => ({
  ...(await load<typeof import("@multica/core/knowledge")>()),
  useKnowledge: () => ({
    status: { data: { healthy: true } },
    resources: {
      data: {
        result: {
          result: [
            {
              uri: "viking://resources/workspaces/ws/notes",
              name: "notes",
              isDir: true,
            },
          ],
        },
      },
      refetch: vi.fn(),
    },
    search: {},
    imports: { data: [] },
    upload: { mutate: vi.fn() },
    remove: { mutateAsync: state.remove },
    importURL: { mutateAsync: state.importURL },
  }),
}));

function Page() {
  return (
    <I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { workspace } }}>
      <KnowledgePage />
    </I18nProvider>
  );
}

describe("knowledge curation flow", () => {
  beforeEach(() => {
    state.role = "owner";
    state.wsId = "ws";
    state.remove.mockReset().mockResolvedValue({});
    state.importURL.mockReset();
  });
  afterEach(cleanup);

  it("requires confirmation before deleting and lets the owner cancel", async () => {
    render(<Page />);
    fireEvent.click(screen.getByRole("button", { name: "删除 notes" }));
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    expect(state.remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(state.remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "删除 notes" }));
    fireEvent.click(screen.getByRole("button", { name: /^删除$/ }));
    await waitFor(() =>
      expect(state.remove).toHaveBeenCalledExactlyOnceWith({
        uri: "viking://resources/workspaces/ws/notes",
        recursive: true,
      }),
    );
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
  });

  it("hides deletion from ordinary members", () => {
    state.role = "member";
    render(<Page />);
    expect(screen.queryByRole("button", { name: "删除 notes" })).toBeNull();
  });

  it("reuses the import request identity after an uncertain network result", async () => {
    state.importURL
      .mockRejectedValueOnce(new Error("网络中断，请重试"))
      .mockResolvedValueOnce({ id: "job", status: "indexing" });
    render(<Page />);
    const input = screen.getByLabelText("网页地址");
    fireEvent.change(input, {
      target: { value: "https://example.com/article" },
    });
    fireEvent.submit(input.closest("form")!);
    await screen.findByText("网络中断，请重试");
    expect(input).toHaveValue("https://example.com/article");
    fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(state.importURL).toHaveBeenCalledTimes(2));
    expect(state.importURL.mock.calls[0]?.[0]).toEqual(
      state.importURL.mock.calls[1]?.[0],
    );
    await waitFor(() => expect(input).toHaveValue(""));
  });

  it("clears the previous workspace import draft and delete confirmation on switch", () => {
    const page = render(<Page />);
    fireEvent.change(screen.getByLabelText("网页地址"), {
      target: { value: "https://example.com/private" },
    });
    fireEvent.click(screen.getByRole("button", { name: "删除 notes" }));
    state.wsId = "other";
    page.rerender(<Page />);
    expect(screen.getByLabelText("网页地址")).toHaveValue("");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(state.remove).not.toHaveBeenCalled();
  });
});
