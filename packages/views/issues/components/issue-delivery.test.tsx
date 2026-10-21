// @vitest-environment jsdom
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
const mocks = vi.hoisted(() => ({ get: vi.fn(), confirm: vi.fn(), act: vi.fn() }));
vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/api", () => ({ api: { getIssueDeliveries: mocks.get, confirmIssuePlan: mocks.confirm, actIssueDelivery: mocks.act } }));
import { IssueDelivery } from "./issue-delivery";
const delivery = { id: "delivery", issue_id: "issue", source_commit: "a".repeat(40), source_branch: "feature/example", base_commit: "b".repeat(40), phase: "awaiting_review", revision: 4, evidence: [{ name: "技能链接跳转", status: "passed", evidence: "真实页面往返通过" }], error: "", candidate_id: "tested-candidate", candidate_url: "http://int.example.test", queue_ahead: 0, approved_by: null, approved_at: null, created_at: "2026-10-20T10:00:00Z", updated_at: "2026-10-20T10:00:00Z" };
const snapshot = { plan: null, deliveries: [delivery], can_confirm: true };
function render(){const client=new QueryClient({defaultOptions:{queries:{retry:false}}});renderWithI18n(<QueryClientProvider client={client}><IssueDelivery issueId="issue" /></QueryClientProvider>);return client;}
beforeEach(()=>{vi.clearAllMocks();mocks.get.mockResolvedValue(snapshot);mocks.act.mockResolvedValue({...snapshot,deliveries:[{...delivery,phase:"approved",revision:5}]});});
it("requires a separate production confirmation and binds it to the reviewed revision",async()=>{
 render();fireEvent.click(await screen.findByRole("button",{name:"Approve production"}));expect(mocks.act).not.toHaveBeenCalled();fireEvent.click(screen.getByRole("button",{name:/^Confirm$/}));await waitFor(()=>expect(mocks.act).toHaveBeenCalledWith("issue","delivery",4,"approve"));
});
it("shows test evidence and explains that shared int may have changed",async()=>{
 render();fireEvent.click(await screen.findByText("测试与验证摘要"));expect(screen.getByText("真实页面往返通过")).toBeInTheDocument();expect(screen.getByText(/联调环境会供其他任务使用/)).toBeInTheDocument();
});
it("never lets a stale plan start implementation",async()=>{
 mocks.get.mockResolvedValue({...snapshot,deliveries:[],plan:{id:"plan",comment_id:"comment",comment_revision:2,body:"完整计划",stale:true,confirmed_by:null,confirmed_at:null}});render();expect(await screen.findByRole("button",{name:"Confirm plan and start"})).toBeDisabled();expect(mocks.confirm).not.toHaveBeenCalled();
});
it("does not offer an approval action to task identities",async()=>{
 mocks.get.mockResolvedValue({...snapshot,can_confirm:false});render();await screen.findByText("联调通过，等待生产确认");expect(screen.queryByRole("button",{name:"Approve production"})).not.toBeInTheDocument();
});
it("shows the user's exact words and interpreted scope without authorizing anything",async()=>{
 mocks.get.mockResolvedValue({...snapshot,authorizations:[{id:"auth",kind:"release",scope:"只发布菜单修复",revoked:false,comment_snapshot:{content:"测好没问题就上线",revision:1,author_id:"user"},created_at:"2026-10-21T00:00:00Z"}]});
 render();fireEvent.click(await screen.findByText("Authorization basis"));expect(screen.getByText("测好没问题就上线")).toBeInTheDocument();expect(screen.getByText("只发布菜单修复")).toBeInTheDocument();expect(mocks.act).not.toHaveBeenCalled();
});
