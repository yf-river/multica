// @vitest-environment jsdom
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiClient, setApiInstance } from "@multica/core/api";
import type { AgentTask, Comment } from "@multica/core/types";
import { useTaskSupplementDraftStore } from "@multica/core/issues/stores/task-supplement-draft-store";
import { renderWithI18n } from "../../test/i18n";
import { TaskSupplementButton, TaskSupplementDialogs } from "./task-supplement-dialog";

const api = new ApiClient("http://localhost");
const task = { id:"run-1", issue_id:"issue-1", status:"running", can_supplement:true, supplement_capability:"task-supplement-v1" } as AgentTask;
function mount(current=task) {
 const client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});
 return renderWithI18n(<QueryClientProvider client={client}><TaskSupplementButton task={current} issueId="issue-1"/><TaskSupplementDialogs tasks={[current]} issueId="issue-1"/></QueryClientProvider>, {locale:"zh-Hans"});
}
beforeEach(()=>{setApiInstance(api);useTaskSupplementDraftStore.setState({drafts:{}})});
afterEach(()=>{cleanup();vi.restoreAllMocks()});

describe("additional guidance for an exact run",()=>{
 it("hides the action unless the running task negotiated support and permission",()=>{
  for (const current of [{...task,can_supplement:false},{...task,supplement_capability:undefined},{...task,status:"completed" as const}]) {
   const view=mount(current);
   expect(screen.queryByRole("button",{name:"补充消息"})).toBeNull();
   view.unmount();
  }
 });
 it("keeps failed input and retries unchanged text with the same request ID",async()=>{
  const send=vi.spyOn(api,"createTaskSupplement").mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({id:"comment-1",author_type:"member",author_id:"user-1",content:"检查登录",created_at:new Date().toISOString(),type:"comment"} as Comment);
  mount();
  fireEvent.click(screen.getByRole("button",{name:"补充消息"}));
  fireEvent.change(screen.getByRole("textbox"),{target:{value:"检查登录"}});
  fireEvent.click(screen.getByRole("button",{name:"发送"}));
  await waitFor(()=>expect(screen.getByRole("alert")).toBeInTheDocument());
  expect(screen.getByRole("textbox")).toHaveValue("检查登录");
  fireEvent.click(screen.getByRole("button",{name:"发送"}));
  await waitFor(()=>expect(send).toHaveBeenCalledTimes(2));
  expect(send.mock.calls[0]).toEqual(send.mock.calls[1]);
  expect(send.mock.calls[0]?.slice(0,3)).toEqual(["issue-1","run-1","检查登录"]);
  await waitFor(()=>expect(useTaskSupplementDraftStore.getState().drafts[task.id]).toBeUndefined());
 });
 it("preserves an open draft when the run ends and disables sending",async()=>{
  const send=vi.spyOn(api,"createTaskSupplement");
  act(()=>{useTaskSupplementDraftStore.getState().open(task.id,"issue-1");useTaskSupplementDraftStore.getState().setContent(task.id,"issue-1","保留这个输入")});
  mount({...task,status:"completed"});
  expect(screen.getByRole("textbox")).toHaveValue("保留这个输入");
  expect(screen.getByRole("button",{name:"发送"})).toBeDisabled();
  expect(screen.getByRole("button",{name:"复制内容"})).toBeInTheDocument();
  expect(send).not.toHaveBeenCalled();
  expect(useTaskSupplementDraftStore.getState().drafts[task.id]?.content).toBe("保留这个输入");
 });
});
