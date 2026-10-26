// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { useRuntimeInputDraftStore as store } from "./input-draft-store";
import { setCurrentWorkspace } from "../platform/workspace-storage";

beforeEach(async () => {
  setCurrentWorkspace("runtime-input-test", "workspace");
  await Promise.resolve();
  store.setState({ drafts: {} });
});
describe("runtime input retry identity", () => {
  it("retains the request identity through persistence and rehydration", async () => {
    const first = store.getState().reserve("task", "command", "/goal pause");
    const storage = store.persist.getOptions().storage!;
    const persisted = await storage.getItem("multica_runtime_input_drafts");
    expect(persisted).toBeTruthy();
    store.setState({ drafts: {} });
    await storage.setItem("multica_runtime_input_drafts", persisted!);
    await store.persist.rehydrate();
    expect(store.getState().reserve("task", "command", "/goal pause").id).toBe(first.id);
  });
  it("isolates tasks and protects a newer intent from an older response", () => {
    const first = store.getState().reserve("task", "command", "/goal pause");
    const newer = store.getState().reserve("task", "command", "/goal resume");
    const other = store.getState().reserve("other-task", "command", "/goal pause");
    store.getState().clear("task", first.id);
    expect(store.getState().drafts.task?.id).toBe(newer.id);
    expect(other.id).not.toBe(first.id);
    store.getState().clear("task", newer.id);
    expect(store.getState().reserve("task", "command", "/goal resume").id).not.toBe(newer.id);
  });
  it("isolates retry tokens when the user changes workspace", async () => {
    const first = store.getState().reserve("task", "command", "/goal pause");
    setCurrentWorkspace("runtime-input-other", "other");
    await Promise.resolve();
    expect(store.getState().drafts.task).toBeUndefined();
    const second = store.getState().reserve("task", "command", "/goal pause");
    expect(second.id).not.toBe(first.id);
    setCurrentWorkspace("runtime-input-test", "workspace");
    await Promise.resolve();
    expect(store.getState().drafts.task?.id).toBe(first.id);
  });
});
