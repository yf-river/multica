import { describe, expect, it } from "vitest";
import {
  actorIssuesViewStore,
  myIssuesViewStore,
} from "./scoped-issue-view-stores";

describe("scoped Issue view stores", () => {
  it("preserves each current initial view and independent scope", () => {
    expect(actorIssuesViewStore.getState()).toMatchObject({
      viewMode: "list",
      scope: "assigned",
    });
    expect(myIssuesViewStore.getState()).toMatchObject({
      viewMode: "board",
      scope: "assigned",
    });

    actorIssuesViewStore.getState().setScope("created");
    myIssuesViewStore.getState().setScope("agents");

    expect(actorIssuesViewStore.getState().scope).toBe("created");
    expect(myIssuesViewStore.getState().scope).toBe("agents");
  });
});
