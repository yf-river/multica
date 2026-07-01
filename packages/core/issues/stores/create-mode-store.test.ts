import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  openCreateIssueWithPreference,
  useCreateModeStore,
} from "./create-mode-store";
import { useModalStore } from "../../modals";

describe("openCreateIssueWithPreference", () => {
  beforeEach(() => {
    useModalStore.getState().close();
  });

  afterEach(() => {
    useModalStore.getState().close();
  });

  it("opens quick-create-issue when last mode is agent", () => {
    useCreateModeStore.getState().setLastMode("agent");
    openCreateIssueWithPreference();
    expect(useModalStore.getState().modal).toBe("quick-create-issue");
    expect(useModalStore.getState().data).toBeNull();
  });

  it("always opens quick-create-issue even when last mode is manual", () => {
    useCreateModeStore.getState().setLastMode("manual");
    openCreateIssueWithPreference();
    expect(useModalStore.getState().modal).toBe("quick-create-issue");
  });

  it("forwards seed data to quick-create-issue", () => {
    useCreateModeStore.getState().setLastMode("manual");
    openCreateIssueWithPreference({ project_id: "p1" });
    expect(useModalStore.getState().modal).toBe("quick-create-issue");
    expect(useModalStore.getState().data).toEqual({ project_id: "p1" });
  });
});
