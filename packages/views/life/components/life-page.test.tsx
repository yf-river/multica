import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { LifePage } from "./life-page";

const actions = vi.hoisted(() => ({ confirm: vi.fn(), update: vi.fn(), downgrade: vi.fn(), archive: vi.fn(), remove: vi.fn(), retryJob: vi.fn() }));

vi.mock("../../i18n", async () => {
  const resource = (await import("../../locales/zh-Hans/life.json")).default;
  return {
    useT: () => ({
      t: (selector: (value: typeof resource) => string, values?: Record<string, unknown>) => {
        let result = selector(resource);
        for (const [key, value] of Object.entries(values ?? {})) result = result.replace(`{{${key}}}`, String(value));
        return result;
      },
    }),
  };
});
vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", () => ({ api: { retryLifeCognitionJob: actions.retryJob } }));
vi.mock("@multica/core/chat", () => ({
  useChatStore: (selector: (value: unknown) => unknown) => selector({
    setOpen: vi.fn(),
    setExpanded: vi.fn(),
  }),
}));
vi.mock("@multica/core/life", () => ({
  lifeKeys: { chronicle: () => ["chronicle"], proposals: () => ["proposals"], identity: () => ["identity"], relationships: () => ["relationships"], materials: () => ["materials"], thoughts: () => ["thoughts"], topics: () => ["topics"], commitments: () => ["commitments"], modules: () => ["modules"], observers: () => ["observers"], observationSeat: () => ["seat"], jobs: () => ["jobs"], upgrades: () => ["upgrades"], policy: () => ["policy"] },
  lifeMemoryListOptions: () => ({ queryKey: ["memories"] }),
  companionProfileOptions: () => ({ queryKey: ["companion"] }),
  lifeProposalListOptions: () => ({ queryKey: ["proposals"] }),
  lifeExperimentListOptions: () => ({ queryKey: ["experiments"] }),
  lifeProactiveCheckListOptions: () => ({ queryKey: ["checks"] }),
  lifeChronicleListOptions: () => ({ queryKey: ["chronicle"] }),
  lifeIdentityListOptions: () => ({ queryKey: ["identity"] }),
  lifeRelationshipListOptions: () => ({ queryKey: ["relationships"] }),
  lifeMaterialListOptions: () => ({ queryKey: ["materials"] }),
  lifeInternalThoughtListOptions: () => ({ queryKey: ["thoughts"] }),
  lifeTopicListOptions: () => ({ queryKey: ["topics"] }),
  lifeCommitmentListOptions: () => ({ queryKey: ["commitments"] }),
  lifeModuleListOptions: () => ({ queryKey: ["modules"] }),
  lifeObserverListOptions: () => ({ queryKey: ["observers"] }),
  lifeObservationSeatOptions: () => ({ queryKey: ["seat"] }),
  lifeCognitionJobListOptions: () => ({ queryKey: ["jobs"] }),
  lifeUpgradeEvaluationListOptions: () => ({ queryKey: ["upgrades"] }),
  lifeProactivePolicyOptions: () => ({ queryKey: ["policy"] }),
  useConfirmLifeMemory: () => ({ mutate: actions.confirm, isPending: false }),
  useUpdateLifeMemory: () => ({ mutate: actions.update, isPending: false }),
  useDowngradeLifeMemory: () => ({ mutate: actions.downgrade, isPending: false }),
  useArchiveLifeMemory: () => ({ mutate: actions.archive, isPending: false }),
  useDeleteLifeMemory: () => ({ mutate: actions.remove, isPending: false }),
  useConfirmLifeProposal: () => ({ mutate: vi.fn(), isPending: false }),
  useStopLifeExperimentRound: () => ({ mutate: vi.fn(), isPending: false }),
  useReviewLifeExperimentRound: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
		if (queryKey[0] === "life-memory-revisions") return { isLoading: false, data: undefined };
    if (queryKey[0] === "memories") return { isLoading: false, data: { memories: [{
      id: "memory-1", kind: "weak_signal", status: "candidate", content: "最近出现离职冲动，但尚未形成离职决定",
      confidence: 0.62, urgency: 0.3, uncertainty: "目前只有一次表达，需要继续观察",
      valid_from: null, valid_to: null, confirmed_at: null, created_by_type: "agent", created_by_id: "agent-1",
      created_at: "", updated_at: "", evidence: [{ source_type: "chat_message", source_id: "message-1", excerpt: "我不想干了", observed_at: "" }],
    }] } };
    if (queryKey[0] === "companion") return { isLoading: false, data: { profile: { agent_id: "agent-1" } } };
    if (queryKey[0] === "proposals") return { isLoading: false, data: { proposals: [] } };
    if (queryKey[0] === "experiments") return { isLoading: false, data: { experiments: [], rounds: [] } };
    if (queryKey[0] === "checks") return { isLoading: false, data: { checks: [] } };
    if (queryKey[0] === "policy") return { isLoading: false, data: { enabled: true, timezone: "Asia/Shanghai", quiet_hours: {}, minimum_interval_hours: 12, next_check_at: "", unanswered_count: 0 } };
    if (queryKey[0] === "observers") return { isLoading: false, data: { observers: [] } };
    if (queryKey[0] === "seat") return { isLoading: false, data: { topics: [], judgements: [] } };
    if (queryKey[0] === "workspaces") return { isLoading: false, data: [] };
    if (queryKey[0] === "jobs") return { isLoading: false, data: { jobs: [{ id: "job-1", job_type: "understand_materials", status: "cancelled", input: {}, output: null, scheduled_at: "", completed_at: null, error: "timeout", attempt: 3, max_attempts: 3 }] } };
    if (queryKey[0] === "upgrades") return { isLoading: false, data: { evaluations: [] } };
    if (queryKey[0] === "identity") return { isLoading: false, data: { versions: [{ id: "identity-1", version: 3, status: "active", stable_core: { traits: ["热烈", "直接"], position: "站在用户一边，但不永远同意" }, relationship_contract: { conflict: "保留分歧但不离开", follow_up: "搭子主动回看" }, growth_profile: {}, expression_profile: {}, interests: [], change_reason: "共同确认" }] } };
    if (queryKey[0] === "relationships") return { isLoading: false, data: { events: [{ id: "event-1", event_type: "agreement", status: "open", context: "忙完后一起复盘", user_position: "希望被主动记得", companion_position: "主动回看但允许拒绝" }] } };
    if (queryKey[0] === "materials") return { isLoading: false, data: { materials: [] } };
    if (queryKey[0] === "thoughts") return { isLoading: false, data: { thoughts: [] } };
    if (queryKey[0] === "topics") return { isLoading: false, data: { topics: [] } };
    if (queryKey[0] === "commitments") return { isLoading: false, data: { commitments: [] } };
    return { isLoading: false, data: { entries: [] } };
  },
  useMutation: ({ mutationFn }: { mutationFn: (value: never) => unknown }) => ({ mutate: (value: never) => mutationFn(value), isPending: false }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

describe("LifePage memory governance", () => {
  it("shows evidence, confidence and uncertainty before confirmation", () => {
    render(<LifePage />);
    expect(screen.getByText("最近出现离职冲动，但尚未形成离职决定")).toBeInTheDocument();
    expect(screen.getByText("可信度 62%")).toBeInTheDocument();
    expect(screen.getByText("不确定：目前只有一次表达，需要继续观察")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    expect(actions.confirm).toHaveBeenCalledWith("memory-1");
  });

  it("requires a second explicit action before permanent deletion", () => {
    render(<LifePage />);
    fireEvent.click(screen.getByRole("button", { name: "永久删除" }));
    expect(actions.remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认永久删除" }));
    expect(actions.remove).toHaveBeenCalledWith("memory-1");
  });

  it("shows identity and relationship state as human language", () => {
    render(<LifePage />);
    expect(screen.getByText("热烈、直接")).toBeInTheDocument();
    expect(screen.getByText("关系位置")).toBeInTheDocument();
    expect(screen.getByText("共同约定 · 待商量")).toBeInTheDocument();
    expect(screen.queryByText("agreement · open")).not.toBeInTheDocument();
  });

  it("offers an explicit retry for exhausted cognition", () => {
    render(<LifePage />);
    fireEvent.click(screen.getByRole("tab", { name: "观察席" }));
    expect(screen.getByText("连续处理失败，这部分内容还没有进入长期理解。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重新处理" }));
    expect(actions.retryJob).toHaveBeenCalledWith("job-1");
  });
});
