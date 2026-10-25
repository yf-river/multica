// @vitest-environment jsdom

import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { EMPTY_SUPERVISION } from "@multica/core/supervision/schema";

const mocks = vi.hoisted(() => ({ snapshot: vi.fn(), list: vi.fn(), check: vi.fn(), detail: vi.fn(), evidence: vi.fn(), preview: vi.fn(), replace: vi.fn(), params: new URLSearchParams() }));
vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => "ws", useWorkspacePaths: () => ({ runReviews: () => "/ws/run-reviews", issueDetail: (id: string) => `/ws/issues/${id}` }) }));
vi.mock("../../navigation", () => ({ useNavigation: () => ({ pathname: "/ws/run-reviews", searchParams: mocks.params, replace: mocks.replace }), AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }));
vi.mock("@multica/core/api", () => ({ api: { getSupervision: mocks.snapshot, listSupervision: mocks.list, checkSupervision: mocks.check, getSupervisionSubject: mocks.detail, getSupervisionEvidence: mocks.evidence, previewCommentTriggers: mocks.preview, listProjects: async () => ({ projects: [] }), listAgents: async () => [] } }));
import { QualitySupervision, SupervisionEvidencePanel } from "./quality-supervision";

function renderPage() {
  return renderWithI18n(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><QualitySupervision /></QueryClientProvider>);
}
beforeEach(() => { vi.clearAllMocks(); mocks.params = new URLSearchParams(); mocks.snapshot.mockResolvedValue({ ...EMPTY_SUPERVISION, can_manage: true }); mocks.list.mockResolvedValue({ items: [], has_more: false, offset: 0 }); });
describe("quality supervision", () => {
  // 1. Partial collection is explicit and opens its immutable material without leaving the task detail.
  it("opens partial trace evidence while keeping coverage gaps visible", async () => {
    mocks.params = new URLSearchParams({ selected: "issue" });
    mocks.detail.mockResolvedValue({
      title: "轨迹对照", description: "", assignee_type: null, assignee_id: null,
      subject: { summary: "等待核验", efficiency: "uncertain", correctness: "uncertain", gaps: [], desired_version: 1, reviewed_version: 0, last_checked_at: null, error: "", failures: 0 },
      findings: [], notes: [], actions: [], batches: [], investigations: [], coverage: { materials: 1, covered: 0, runs: 1 },
      trace_materials: [{ id: "material", run_id: "run", runtime: "codebuddy", status: "partial", material_version: 1, queried_at: null, gaps: ["request identity missing"], matched_requests: 1, differences: 0, local_snapshots: 1, preparation_http_requests: 2, preparation_duration_ms: 40 }],
    });
    mocks.evidence.mockResolvedValue({ kind: "trace_usage", id: "material", issue_id: "issue", text: "固定版本材料", offset: 0, end: 6, length: 6, deleted: false });
    renderPage();
    fireEvent.click(await screen.findByText(/codebuddy · AgentLens 部分/));
    expect(screen.getByText("request identity missing")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "查看材料证据" }));
    expect(await screen.findByText("固定版本材料")).toBeInTheDocument();
    expect(mocks.evidence).toHaveBeenCalledWith("trace_usage", "material", 0, undefined);
    expect(mocks.replace).not.toHaveBeenCalled();
  });
  it("opens the cited batch snapshot without paging into edited live content", async () => {
    mocks.params = new URLSearchParams({ evidence_kind: "trace", evidence_id: "source", evidence_batch: "batch-snapshot" });
    mocks.evidence.mockResolvedValue({ kind: "trace", id: "source", issue_id: "issue", snapshot: true, batch_id: "batch-snapshot", text: "审查当时的片段", offset: 20000, end: 28000, length: 100000, deleted: false });
    renderWithI18n(<QueryClientProvider client={new QueryClient()}><SupervisionEvidencePanel /></QueryClientProvider>);
    expect(await screen.findByText("审查当时的片段")).toBeInTheDocument();
    expect(mocks.evidence).toHaveBeenCalledWith("trace", "source", 0, "batch-snapshot");
    expect(screen.getByText("20000–28000 / 100000")).toBeInTheDocument();
    expect(screen.getByText("Saved review excerpt. Unsaved portions are not shown here.")).toBeInTheDocument();
    for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
  });
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
    expect(screen.getByText("10 材料已覆盖")).toBeInTheDocument();
    expect(screen.getByText("90 暂无执行记录")).toBeInTheDocument();
  });
  it("surfaces repeated failures and offers a failed filter", async () => {
    mocks.snapshot.mockResolvedValue({
      ...EMPTY_SUPERVISION,
      config: { ...EMPTY_SUPERVISION.config, enabled: true, agent_id: "a1", inventory_complete: true },
      can_manage: true,
      summary: { ...EMPTY_SUPERVISION.summary, total: 2, covered: 1, failed: 1 },
    });
    mocks.list.mockResolvedValue({
      items: [
        { issue_id: "broken", identifier: "AIS-9", title: "重试失败的任务", business_status: "done", status_category: "done", project_id: null, desired_version: 1, reviewed_version: 1, summary: "", efficiency: "uncertain", correctness: "uncertain", run_count: 1, gaps: [], last_checked_at: "2026-10-20T03:00:00Z", error: "", failures: 3, stopped: false, running: false, open_findings: 0, round_sequence: 1, pending_materials: 2 },
        { issue_id: "ok", identifier: "AIS-10", title: "正常覆盖的任务", business_status: "done", status_category: "done", project_id: null, desired_version: 1, reviewed_version: 1, summary: "", efficiency: "clear", correctness: "clear", run_count: 1, gaps: [], last_checked_at: "2026-10-20T03:00:00Z", error: "", failures: 0, stopped: false, running: false, open_findings: 0, round_sequence: 1, pending_materials: 0 },
      ],
      has_more: false,
      offset: 0,
    });
    renderPage();
    expect(await screen.findByText("1 failed checks")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Check failed" })).toBeInTheDocument();
    expect(screen.getByText("已停止自动检查，需要恢复")).toBeInTheDocument();
    expect(mocks.check).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "恢复失败任务" }));
    await waitFor(() => expect(mocks.check).toHaveBeenCalledWith({ retry_failed: true }));
    expect(screen.getByRole("button", { name: /AIS-10/ })).toHaveTextContent("未发现问题");
  });
  it("keeps unfinished tasks waiting and separates coverage from findings", async () => {
    mocks.snapshot.mockResolvedValue({
      ...EMPTY_SUPERVISION,
      config: { ...EMPTY_SUPERVISION.config, enabled: true, agent_id: "a1", inventory_complete: true, interval_seconds: 300 },
      can_manage: true,
      summary: { ...EMPTY_SUPERVISION.summary, total: 3, covered: 1, pending: 1, waiting: 1 },
    });
    const base = { business_status: "done", status_category: "done", project_id: null, desired_version: 1, reviewed_version: 1, summary: "", efficiency: "uncertain", correctness: "uncertain", gaps: [], last_checked_at: null, error: "", failures: 0, stopped: false, running: false, open_findings: 0, pending_materials: 0 };
    mocks.list.mockResolvedValue({
      items: [
        { ...base, issue_id: "dev", identifier: "AIS-1", title: "开发中的任务", business_status: "in_progress", status_category: "in_progress", run_count: 2, round_sequence: null },
        { ...base, issue_id: "wait", identifier: "AIS-2", title: "刚完成的任务", run_count: 1, round_sequence: 1, pending_materials: 24 },
        { ...base, issue_id: "covered", identifier: "AIS-3", title: "已审查的任务", run_count: 1, round_sequence: 1, last_checked_at: "2026-10-20T03:00:00Z" },
      ],
      has_more: false,
      offset: 0,
    });
    renderPage();
    expect(await screen.findByRole("button", { name: /AIS-1/ })).toHaveTextContent("等待任务完成");
    expect(screen.getByRole("button", { name: /AIS-2/ })).toHaveTextContent("待监督");
    expect(screen.getByRole("button", { name: /AIS-3/ })).toHaveTextContent("已监督");
    expect(screen.getByRole("button", { name: /AIS-3/ })).toHaveTextContent("材料已覆盖，未发现问题");
    expect(screen.queryByRole("button", { name: /AIS-1/ })).not.toHaveTextContent("未发现问题");
  });
  it("opens cited evidence inside the detail without leaving the page", async () => {
    mocks.params = new URLSearchParams({ selected: "issue" });
    mocks.detail.mockResolvedValue({
      title: "证据核对", description: "只读核验", assignee_type: "agent", assignee_id: "agent-1",
      subject: { summary: "发现问题", efficiency: "problem", correctness: "problem", gaps: [], desired_version: 1, reviewed_version: 1, last_checked_at: "2026-10-20T03:00:00Z", error: "", failures: 0, stopped: false },
      findings: [{ id: "f1", title: "字段错误", description: "实际取 UserID", impact: "租户错误", recommendation: "改为 TenantID", confidence: "confirmed", evidence: [{ kind: "trace", id: "source-1", batch_id: "batch-1", run_id: "run-1" }], status: "open", human_resolution: false, first_seen_at: "2026-10-20T03:00:00Z", last_seen_at: "2026-10-20T03:00:00Z", snoozed_until: null }],
      notes: [], actions: [], batches: [], coverage: { materials: 1, covered: 1, runs: 1 }, investigations: [],
    });
    mocks.evidence.mockResolvedValue({ kind: "trace", id: "source-1", issue_id: "issue", run_id: "run-1", batch_id: "batch-1", snapshot: true, text: "审查时的证据片段", offset: 0, end: 8, length: 8, deleted: false });
    renderPage();
    expect(await screen.findByRole("button", { name: "重新审查" })).toBeDisabled();
    expect(screen.getByText("Resume supervision before reviewing this task again.")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("查看证据、影响和建议"));
    fireEvent.click(screen.getByRole("button", { name: /View evidence/ }));
    expect(await screen.findByText("审查时的证据片段")).toBeInTheDocument();
    expect(mocks.evidence).toHaveBeenCalledWith("trace", "source-1", 0, "batch-1");
    expect(mocks.replace).not.toHaveBeenCalled();
    const records = screen.getByRole("link", { name: "Open in run records" });
    expect(records.getAttribute("href")).toContain("evidence_id=source-1");
    expect(records.getAttribute("href")).toContain("tab=records");
  });
  it("explains why feedback cannot be sent before the trigger preview", async () => {
    mocks.params = new URLSearchParams({ selected: "issue" });
    mocks.detail.mockResolvedValue({
      title: "反馈核对", description: "只读核验", assignee_type: "agent", assignee_id: "agent-1",
      subject: { summary: "发现问题", efficiency: "problem", correctness: "problem", gaps: [], desired_version: 1, reviewed_version: 1, last_checked_at: "2026-10-20T03:00:00Z", error: "", failures: 0, stopped: false },
      findings: [{ id: "f1", title: "字段错误", description: "实际取 UserID", impact: "租户错误", recommendation: "改为 TenantID", confidence: "confirmed", evidence: [], status: "open", human_resolution: false, first_seen_at: "2026-10-20T03:00:00Z", last_seen_at: "2026-10-20T03:00:00Z", snoozed_until: null }],
      notes: [], actions: [], batches: [], coverage: { materials: 1, covered: 1, runs: 1 }, investigations: [],
    });
    mocks.preview.mockResolvedValue({ agents: [{ id: "agent-1", name: "质量监督（DeepSeek Flash）" }], blocked: [] });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "反馈给负责人" }));
    expect(await screen.findByText("Preview trigger targets before sending.")).toBeInTheDocument();
    expect(screen.getByText("Not previewed yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "发送反馈" })).toBeDisabled();
    let finishPreview!: (value: { agents: { id: string; name: string }[]; blocked: never[] }) => void;
    mocks.preview.mockReturnValueOnce(new Promise((resolve) => { finishPreview = resolve; }));
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "正文 A" } });
    fireEvent.click(screen.getByRole("button", { name: "预览触发对象" }));
    await waitFor(() => expect(mocks.preview).toHaveBeenLastCalledWith("issue", "正文 A"));
    fireEvent.change(input, { target: { value: "正文 B" } });
    await act(async () => { finishPreview({ agents: [{ id: "agent-1", name: "旧正文的负责人" }], blocked: [] }); });
    expect(await screen.findByText("Content changed; preview again.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "发送反馈" })).toBeDisabled();
    expect(screen.queryByText("旧正文的负责人")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "预览触发对象" }));
    expect(await screen.findByText("质量监督（DeepSeek Flash）")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "发送反馈" })).toBeEnabled();
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
