import type { PropsWithChildren } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PromptLibraryItem } from "@multica/core/types";
import { promptLibraryKeys } from "./prompt-library-query-keys";
import { usePromptLibraryMutations } from "./use-prompt-library-mutations";

const mocks = vi.hoisted(() => ({
  createPromptLibraryItem: vi.fn(),
  createPromptEvaluationAsset: vi.fn(),
  createPromptEvaluationDatasetFromTraces: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    createPromptLibraryItem: mocks.createPromptLibraryItem,
    createPromptEvaluationAsset: mocks.createPromptEvaluationAsset,
    createPromptEvaluationDatasetFromTraces: mocks.createPromptEvaluationDatasetFromTraces,
  },
}));

vi.mock("sonner", () => ({
  toast: {
    success: mocks.toastSuccess,
    error: mocks.toastError,
  },
}));

vi.mock("../../i18n/use-t", () => ({
  useT: () => ({
    t: (selector: (value: unknown) => unknown) => {
      const pathProxy = (path: string[]): unknown =>
        new Proxy(() => undefined, {
          get: (_target, property) => {
            if (property === Symbol.toPrimitive) return () => path.join(".");
            return pathProxy([...path, String(property)]);
          },
        });
      return String(selector(pathProxy([])));
    },
  }),
}));

const prompt: PromptLibraryItem = {
  id: "prompt-1",
  workspace_id: "workspace-1",
  project_id: null,
  name: "登录失败分析",
  description: "",
  prompt_type: "text",
  content: "分析登录失败原因",
  variables: [],
  tags: [],
  status: "启用",
  version: 1,
  created_by: null,
  created_at: "2026-07-10T00:00:00Z",
  updated_at: "2026-07-10T00:00:00Z",
};

function renderController() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
  const onPromptCreated = vi.fn();
  const onPromptVersionCreated = vi.fn();
  const onPromptDeleted = vi.fn();
  const wrapper = ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  const hook = renderHook(
    () => usePromptLibraryMutations({
      workspaceId: "workspace-1",
      focusedIssueId: "issue-1",
      focusedIssueTaskIds: ["task-1", "task-2"],
      cases: [],
      onPromptCreated,
      onPromptVersionCreated,
      onPromptDeleted,
    }),
    { wrapper },
  );
  return { ...hook, invalidateQueries, onPromptCreated };
}

describe("prompt library mutation controller", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("projects a created prompt into the current list and version caches", async () => {
    mocks.createPromptLibraryItem.mockResolvedValue(prompt);
    const { result, invalidateQueries, onPromptCreated } = renderController();

    await act(() => result.current.createPrompt.mutateAsync({
      name: prompt.name,
      description: prompt.description,
      content: prompt.content,
    }));

    expect(onPromptCreated).toHaveBeenCalledWith(prompt);
    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: promptLibraryKeys.list("workspace-1"),
    });
    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: promptLibraryKeys.versions("workspace-1", prompt.id),
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("page.toast.prompt_created");
  });

  it("keeps trace imports scoped to the focused issue task set", async () => {
    mocks.createPromptEvaluationDatasetFromTraces.mockResolvedValue({ created_count: 2 });
    const { result } = renderController();

    await act(() => result.current.importDatasetFromTraces.mutateAsync("asset-1"));

    expect(mocks.createPromptEvaluationDatasetFromTraces).toHaveBeenCalledWith("asset-1", {
      limit: 2,
      task_ids: ["task-1", "task-2"],
      expected_contains: ["任务", "trace"],
      tags: ["trace导入", "真实执行记录", "issue:issue-1"],
    });
  });

  it("reports failed writes without projecting success", async () => {
    const error = new Error("asset save failed");
    mocks.createPromptEvaluationAsset.mockRejectedValue(error);
    const { result } = renderController();

    await expect(act(() => result.current.createAsset.mutateAsync({
      name: "失败用例集",
      description: "",
      asset_type: "数据集",
      payload: {},
      status: "启用",
    }))).rejects.toThrow("asset save failed");

    expect(mocks.toastError).toHaveBeenCalledWith("asset save failed");
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });
});
