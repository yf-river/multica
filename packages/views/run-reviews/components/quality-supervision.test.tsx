// @vitest-environment jsdom

import { fireEvent, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { EMPTY_SUPERVISION } from "@multica/core/supervision/schema";

const mocks = vi.hoisted(() => ({ snapshot: vi.fn(), list: vi.fn(), check: vi.fn(), detail: vi.fn(), params: new URLSearchParams() }));
vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => "ws", useWorkspacePaths: () => ({ runReviews: () => "/ws/run-reviews", issueDetail: (id: string) => `/ws/issues/${id}` }) }));
vi.mock("../../navigation", () => ({ useNavigation: () => ({ pathname: "/ws/run-reviews", searchParams: mocks.params, replace: vi.fn() }), AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }));
vi.mock("@multica/core/api", () => ({ api: { getSupervision: mocks.snapshot, listSupervision: mocks.list, checkSupervision: mocks.check, getSupervisionSubject: mocks.detail, listProjects: async () => ({ projects: [] }), listAgents: async () => [] } }));
import { QualitySupervision } from "./quality-supervision";

function renderPage() {
  return renderWithI18n(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><QualitySupervision /></QueryClientProvider>);
}
beforeEach(() => { vi.clearAllMocks(); mocks.params = new URLSearchParams(); mocks.snapshot.mockResolvedValue({ ...EMPTY_SUPERVISION, can_manage: true }); mocks.list.mockResolvedValue({ items: [], has_more: false, offset: 0 }); });
describe("quality supervision", () => {
  it("does not enable or create execution when the page is opened", async () => {
    renderPage();
    expect(await screen.findByRole("button", { name: "配置并启用" })).toBeInTheDocument();
    expect(mocks.check).not.toHaveBeenCalled();
  });
  it("shows an unavailable service as failure instead of a clean report", async () => {
    mocks.snapshot.mockRejectedValue(new Error("contract unavailable")); renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("无法读取监督状态");
    expect(screen.queryByText("未发现问题")).not.toBeInTheDocument();
  });
  it("separates covered content from tasks without execution records", async () => {
    mocks.snapshot.mockResolvedValue({ ...EMPTY_SUPERVISION, config: { ...EMPTY_SUPERVISION.config, enabled: true, agent_id: "a1", inventory_complete: true }, summary: { ...EMPTY_SUPERVISION.summary, total: 100, covered: 10, no_runs: 90 } });
    renderPage();
    expect(await screen.findByText("共 100 个任务")).toBeInTheDocument();
    expect(screen.getByText("10 已覆盖当前内容")).toBeInTheDocument();
    expect(screen.getByText("90 暂无执行记录")).toBeInTheDocument();
  });
  it("does not describe search output or index entries as full source coverage", async () => {
    mocks.params.set("selected", "issue");
    const record = (id: string, tool: string, path: string, end: number, length: number) => ({
      id, batch_id: "batch", created_at: "2026-10-20T03:00:00Z", request: { reason: id },
      result: { tool, path, source_label: "关联项目日志", offset: 0, end, length, truncated: end < length },
    });
    mocks.detail.mockResolvedValue({
      title: "调查范围", description: "只读核验", assignee_type: null, assignee_id: null,
      subject: { summary: "核验记录", efficiency: "clear", correctness: "clear", gaps: [], desired_version: 1, reviewed_version: 1, last_checked_at: "2026-10-20T03:00:00Z", error: "", failures: 0 },
      findings: [], notes: [], actions: [], batches: [], coverage: { materials: 1, covered: 1, runs: 1 },
      investigations: [record("读取片段", "supervision_read", "trace.log", 8000, 11200058), record("定位FINAL", "supervision_search", "trace.log", 109, 109), record("列材料", "supervision_material", "index", 20, 35)],
    });
    renderPage();
    await screen.findByText("定位FINAL");
    fireEvent.click(screen.getByText("Investigation"));
    expect(screen.getByText("Source 0–8000 / 11200058 characters")).toBeInTheDocument();
    expect(screen.getByText("Result 0–109 / 109 characters")).toBeInTheDocument();
    expect(screen.getByText("Index 0–20 / 35 entries")).toBeInTheDocument();
    expect(screen.queryByText("Source 0–109 / 109 characters")).not.toBeInTheDocument();
  });
});
