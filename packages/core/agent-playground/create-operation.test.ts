// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { AgentPlaygroundDetail, CreateAgentPlaygroundExperimentRequest } from "../types";
import {
  createAgentPlaygroundExperimentWithRecovery,
  useAgentPlaygroundCreateStore,
  type AgentPlaygroundCreateClient,
} from "./create-operation";

const request: CreateAgentPlaygroundExperimentRequest = {
  name: "Current experiment",
  dataset_asset_id: "asset-1",
  dataset_version_id: "version-1",
  agent_ids: ["agent-1"],
};

const detail = (id: string) => ({ experiment: { id } }) as AgentPlaygroundDetail;

describe("createAgentPlaygroundExperimentWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    useAgentPlaygroundCreateStore.setState({ pending: undefined });
  });

  it("replays the persisted current request after an unknown outcome", async () => {
    const createAgentPlaygroundExperiment = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST playground", true, new Error("lost")))
      .mockResolvedValueOnce(detail("experiment-1"));
    const client: AgentPlaygroundCreateClient = { createAgentPlaygroundExperiment };

    await expect(createAgentPlaygroundExperimentWithRecovery(request, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createAgentPlaygroundExperimentWithRecovery(request, client))
      .resolves.toMatchObject({ experiment: { id: "experiment-1" } });

    const firstKey = createAgentPlaygroundExperiment.mock.calls[0]?.[1];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(createAgentPlaygroundExperiment.mock.calls[1]?.[1]).toBe(firstKey);
    expect(useAgentPlaygroundCreateStore.getState().pending).toBeUndefined();
  });

  it("recovers an older request before starting a changed experiment", async () => {
    useAgentPlaygroundCreateStore.getState().setPending({
      request,
      requestKey: "10000000-0000-4000-8000-000000000011",
      createdAt: Date.now(),
    });
    const createAgentPlaygroundExperiment = vi.fn()
      .mockResolvedValueOnce(detail("experiment-old"))
      .mockResolvedValueOnce(detail("experiment-new"));
    const client: AgentPlaygroundCreateClient = { createAgentPlaygroundExperiment };
    const changed = { ...request, name: "Changed experiment" };

    await expect(createAgentPlaygroundExperimentWithRecovery(changed, client))
      .resolves.toMatchObject({ experiment: { id: "experiment-new" } });
    expect(createAgentPlaygroundExperiment.mock.calls[0]?.[0]).toEqual(request);
    expect(createAgentPlaygroundExperiment.mock.calls[0]?.[1])
      .toBe("10000000-0000-4000-8000-000000000011");
    expect(createAgentPlaygroundExperiment.mock.calls[1]?.[0]).toEqual(changed);
  });
});
