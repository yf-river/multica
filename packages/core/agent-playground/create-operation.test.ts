// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { AgentPlaygroundDetail, CreateAgentPlaygroundExperimentRequest } from "../types";
import { createAgentPlaygroundExperimentWithRecovery } from "./create-operation";

const request: CreateAgentPlaygroundExperimentRequest = {
  name: "Current experiment",
  dataset_asset_id: "asset-1",
  dataset_version_id: "version-1",
  agent_ids: ["agent-1"],
};

const detail = (id: string) => ({ experiment: { id } }) as AgentPlaygroundDetail;
let workspaceSequence = 0;

describe("createAgentPlaygroundExperimentWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    workspaceSequence += 1;
    setCurrentWorkspace(`agent-playground-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
  });

  it("replays the persisted current request after an unknown outcome", async () => {
    const createAgentPlaygroundExperiment = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST playground", true, new Error("lost")))
      .mockResolvedValueOnce(detail("experiment-1"));
    const client = { createAgentPlaygroundExperiment };

    await expect(createAgentPlaygroundExperimentWithRecovery(request, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createAgentPlaygroundExperimentWithRecovery(request, client))
      .resolves.toMatchObject({ experiment: { id: "experiment-1" } });

    const firstKey = createAgentPlaygroundExperiment.mock.calls[0]?.[1];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(createAgentPlaygroundExperiment.mock.calls[1]?.[1]).toBe(firstKey);
  });

  it("recovers an older request before starting a changed experiment", async () => {
    const createAgentPlaygroundExperiment = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old playground", true, new Error("lost")))
      .mockResolvedValueOnce(detail("experiment-old"))
      .mockResolvedValueOnce(detail("experiment-new"));
    const client = { createAgentPlaygroundExperiment };
    const changed = { ...request, name: "Changed experiment" };

    await expect(createAgentPlaygroundExperimentWithRecovery(request, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createAgentPlaygroundExperimentWithRecovery(changed, client))
      .resolves.toMatchObject({ experiment: { id: "experiment-new" } });
    expect(createAgentPlaygroundExperiment.mock.calls[1]).toEqual([
      request, createAgentPlaygroundExperiment.mock.calls[0]?.[1],
    ]);
    expect(createAgentPlaygroundExperiment.mock.calls[2]?.[0]).toEqual(changed);
  });
});
