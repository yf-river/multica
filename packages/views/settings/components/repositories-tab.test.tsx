import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enSettings from "../../locales/zh-Hans/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const workspaceRef = vi.hoisted(() => ({
  current: {
    id: "workspace-1",
    name: "Test Workspace",
    slug: "test-workspace",
    repos: [{ url: "https://github.com/multica-ai/multica" }] as { url: string; description?: string }[],
  },
}));
const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as const }],
}));

vi.mock("@tanstack/react-query", () => ({
  queryOptions: (options: unknown) => options,
  useQuery: (options?: { queryKey?: readonly unknown[] }) =>
    options?.queryKey?.[0] === "projects"
      ? { data: [] }
      : { data: membersRef.current },
  useQueries: () => [],
  useMutation: () => ({ mutate: vi.fn(), isPending: false }),
  useQueryClient: () => ({ setQueryData: vi.fn(), invalidateQueries: vi.fn() }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => workspaceRef.current,
  useWorkspacePaths: () => ({
    projectDetail: (id: string) => `/test-workspace/projects/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
  workspaceKeys: { list: () => ["workspaces"] },
}));

vi.mock("@multica/core/api", () => ({
  api: { updateWorkspace: mockUpdateWorkspace },
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (sel?: (s: { user: { id: string } }) => unknown) =>
      sel ? sel({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { RepositoriesTab } from "./repositories-tab";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

describe("RepositoriesTab — view/edit toggle", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    workspaceRef.current = {
      id: "workspace-1",
      name: "Test Workspace",
      slug: "test-workspace",
      repos: [{ url: "https://github.com/multica-ai/multica" }],
    };
    membersRef.current = [{ user_id: "user-1", role: "owner" }];
  });

  it("展示模式渲染已保存仓库且没有输入框", () => {
    render(<RepositoriesTab />, { wrapper: I18nWrapper });
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("https://github.com/multica-ai/multica")).toBeTruthy();
  });

  it("无改动时保存按钮禁用", () => {
    render(<RepositoriesTab />, { wrapper: I18nWrapper });
    expect(screen.getByRole("button", { name: /^保存$/ })).toBeDisabled();
  });

  it("点击编辑后显示预填 URL 的输入框", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "编辑仓库" }));

    const inputs = screen.getAllByRole("textbox") as HTMLInputElement[];
    expect(inputs[0]!.value).toBe("https://github.com/multica-ai/multica");
  });

  it("编辑后重新启用保存，成功后回到展示模式并禁用", async () => {
    const user = userEvent.setup();
    mockUpdateWorkspace.mockImplementation(async (_id: string, payload: { repos: { url: string; description?: string }[] }) => ({
      ...workspaceRef.current,
      repos: payload.repos,
    }));

    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "编辑仓库" }));
    const input = screen.getAllByRole("textbox")[0]!;
    await user.clear(input);
    await user.type(input, "https://github.com/multica-ai/edited");

    const saveBtn = screen.getByRole("button", { name: /^保存$/ });
    expect(saveBtn).not.toBeDisabled();

    // Simulate the workspace cache resync that the parent provider does
    // after a successful save — `setQueryData` updates the cache and the
    // useCurrentWorkspace hook would yield the new value on the next render.
    mockUpdateWorkspace.mockImplementationOnce(async (_id: string, payload: { repos: { url: string; description?: string }[] }) => {
      workspaceRef.current = { ...workspaceRef.current, repos: payload.repos };
      return workspaceRef.current;
    });

    await user.click(saveBtn);

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalled();
    });

    // After successful save, edit mode is cleared — input gone, Save disabled.
    await waitFor(() => {
      expect(screen.queryByRole("textbox")).toBeNull();
    });
    expect(screen.getByRole("button", { name: /^保存$/ })).toBeDisabled();
  });

  it("新增行默认进入编辑模式", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    expect(screen.queryByRole("textbox")).toBeNull();
    await user.click(screen.getByRole("button", { name: /添加仓库/ }));

    expect(screen.getAllByRole("textbox").length).toBe(2); // url + description
    expect(screen.getByRole("button", { name: /^保存$/ })).not.toBeDisabled();
  });

  it("编辑未改动行后取消，会回到展示模式且不改变 URL 或置脏保存", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "编辑仓库" }));
    expect(screen.getAllByRole("textbox").length).toBe(2);

    await user.click(screen.getByRole("button", { name: "取消编辑" }));

    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("https://github.com/multica-ai/multica")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^保存$/ })).toBeDisabled();
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();
  });

  it("取消已修改编辑行会还原 URL 并退出编辑模式", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "编辑仓库" }));
    const input = screen.getAllByRole("textbox")[0] as HTMLInputElement;
    await user.clear(input);
    await user.type(input, "https://github.com/multica-ai/changed");
    expect(screen.getByRole("button", { name: /^保存$/ })).not.toBeDisabled();

    await user.click(screen.getByRole("button", { name: "取消编辑" }));

    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText("https://github.com/multica-ai/multica")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^保存$/ })).toBeDisabled();
  });

  it("取消新增且未保存的行会直接移除该行", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: /添加仓库/ }));
    expect(screen.getAllByRole("textbox").length).toBe(2);

    await user.click(screen.getByRole("button", { name: "取消编辑" }));

    expect(screen.queryByRole("textbox")).toBeNull();
    // Original persisted row is still there; the new empty row is gone.
    expect(screen.getByText("https://github.com/multica-ai/multica")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^保存$/ })).toBeDisabled();
  });

  it("接受 scp 风格简写，且不被浏览器 URL 校验阻止提交", async () => {
    const user = userEvent.setup();
    mockUpdateWorkspace.mockImplementation(
      async (_id: string, payload: { repos: { url: string; description?: string }[] }) => {
        workspaceRef.current = { ...workspaceRef.current, repos: payload.repos };
        return workspaceRef.current;
      },
    );

    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "编辑仓库" }));
    const input = screen.getAllByRole("textbox")[0] as HTMLInputElement;
    await user.clear(input);
    await user.type(input, "git@github.com:multica-ai/multica.git");

    // type="text" (not "url") so the browser does not run native URL
    // validation; the value reaches the server which has the real check.
    expect(input.type).toBe("text");
    expect(input.validity.valid).toBe(true);

    await user.click(screen.getByRole("button", { name: /^保存$/ }));

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [{ url: "git@github.com:multica-ai/multica.git" }],
      });
    });
  });

  it("删除行后平移跟踪中的编辑索引，避免打开错误行", async () => {
    workspaceRef.current = {
      ...workspaceRef.current,
      repos: [{ url: "https://a.example/repo.git" }, { url: "https://b.example/repo.git" }],
    };
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    // Edit the second row.
    const editButtons = screen.getAllByRole("button", { name: "编辑仓库" });
    await user.click(editButtons[1]!);
    expect((screen.getAllByRole("textbox")[0] as HTMLInputElement).value).toBe(
      "https://b.example/repo.git",
    );

    // Delete the first row. The remaining row should remain in edit mode
    // (its index dropped from 1 → 0).
    const deleteButtons = screen.getAllByRole("button", { name: "删除仓库" });
    await user.click(deleteButtons[0]!);

    const input = screen.getAllByRole("textbox")[0] as HTMLInputElement;
    expect(input.value).toBe("https://b.example/repo.git");
  });

  it("描述字段可编辑并包含在保存 payload 中", async () => {
    workspaceRef.current = {
      ...workspaceRef.current,
      repos: [{ url: "https://github.com/multica-ai/multica", description: "Main app" }],
    };
    const user = userEvent.setup();
    mockUpdateWorkspace.mockImplementation(
      async (_id: string, payload: { repos: { url: string; description?: string }[] }) => {
        workspaceRef.current = { ...workspaceRef.current, repos: payload.repos };
        return workspaceRef.current;
      },
    );

    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    // Description is shown in display mode.
    expect(screen.getByText("Main app")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "编辑仓库" }));
    const inputs = screen.getAllByRole("textbox") as HTMLInputElement[];
    expect(inputs[1]!.value).toBe("Main app");

    await user.clear(inputs[1]!);
    await user.type(inputs[1]!, "Updated description");

    await user.click(screen.getByRole("button", { name: /^保存$/ }));

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [{ url: "https://github.com/multica-ai/multica", description: "Updated description" }],
      });
    });
  });
});
