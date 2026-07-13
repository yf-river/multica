import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it } from "vitest";

import { cleanupDeletedIssueCaches } from "./delete-cache";
import { issueKeys } from "./queries";
import { useRecentContextStore } from "../chat/recent-context-store";

const WS_ID = "ws-a";

beforeEach(() => {
  useRecentContextStore.setState({ byWorkspace: {} });
});

describe("cleanupDeletedIssueCaches — recent context store", () => {
  it("removes the deleted issue without removing recent projects", () => {
    const { recordVisit } = useRecentContextStore.getState();
    recordVisit(WS_ID, { type: "issue", id: "issue-1" });
    recordVisit(WS_ID, { type: "issue", id: "issue-2" });
    recordVisit(WS_ID, { type: "project", id: "project-1" });

    const qc = new QueryClient();
    cleanupDeletedIssueCaches(qc, WS_ID, "issue-1");

    const keys = useRecentContextStore
      .getState()
      .byWorkspace[WS_ID]?.map((entry) => `${entry.type}:${entry.id}`);
    expect(keys).toEqual(["project:project-1", "issue:issue-2"]);
  });

  it("does not touch the recent bucket of an unrelated workspace", () => {
    const { recordVisit } = useRecentContextStore.getState();
    recordVisit(WS_ID, { type: "issue", id: "issue-1" });
    recordVisit("ws-b", { type: "issue", id: "issue-1" });

    const qc = new QueryClient();
    cleanupDeletedIssueCaches(qc, WS_ID, "issue-1");

    const state = useRecentContextStore.getState().byWorkspace;
    expect(state[WS_ID]).toBeUndefined();
    expect(state["ws-b"]?.map((e) => e.id)).toEqual(["issue-1"]);
  });

  it("still removes the cached detail query for the deleted issue", () => {
    const qc = new QueryClient();
    qc.setQueryData(issueKeys.detail(WS_ID, "issue-1"), { id: "issue-1" });

    cleanupDeletedIssueCaches(qc, WS_ID, "issue-1");

    expect(qc.getQueryData(issueKeys.detail(WS_ID, "issue-1"))).toBeUndefined();
  });
});
