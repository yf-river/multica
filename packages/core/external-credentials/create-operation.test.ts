// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, ApiTransportError, getApi, setApiInstance } from "../api";
import { resetAccountState } from "../platform/workspace-storage";
import type { ExternalCredentialProfile } from "../types";
import { createExternalCredentialProfileWithRecovery } from "./create-operation";

const profile = (id: string) => ({ id }) as ExternalCredentialProfile;

describe("createExternalCredentialProfileWithRecovery", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    localStorage.clear();
    resetAccountState();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("persists only a request fingerprint when a token outcome is unknown", async () => {
    vi.spyOn(getApi(), "createExternalCredentialProfile").mockRejectedValue(
      new ApiTransportError("POST credential", true, new Error("lost")),
    );
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng",
      name: "Current",
      token: "secret-sentinel-never-persist",
    })).rejects.toBeInstanceOf(ApiTransportError);

    const stored = localStorage.getItem("multica_credential_profile_create") ?? "";
    expect(stored).not.toContain("secret-sentinel-never-persist");
    expect(stored).toContain("gongfeng");
    expect(stored).toContain("tokenProvided");
  });

  it("recovers a committed profile before accepting changed secret input", async () => {
    const getExternalCredentialProfile = vi.spyOn(getApi(), "getExternalCredentialProfile")
      .mockResolvedValue(profile("profile-1"));
    const createExternalCredentialProfile = vi.spyOn(getApi(), "createExternalCredentialProfile")
      .mockRejectedValueOnce(new ApiTransportError("POST old credential", true, new Error("lost")))
      .mockResolvedValueOnce(profile("profile-2"));
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng", token: "old-secret",
    })).rejects.toBeInstanceOf(ApiTransportError);
    const oldRequestKey = createExternalCredentialProfile.mock.calls[0]?.[1];
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng", token: "new-secret",
    })).resolves.toMatchObject({ id: "profile-2" });
    expect(getExternalCredentialProfile).toHaveBeenCalledWith(oldRequestKey);
    expect(createExternalCredentialProfile).toHaveBeenCalledTimes(2);
  });

  it("creates the current request when the older request did not commit", async () => {
    vi.spyOn(getApi(), "getExternalCredentialProfile")
      .mockRejectedValue(new ApiError("missing", 404, "Not Found"));
    vi.spyOn(getApi(), "createExternalCredentialProfile")
      .mockRejectedValueOnce(new ApiTransportError("POST missing credential", true, new Error("lost")))
      .mockResolvedValueOnce(profile("profile-2"));
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng", token: "old-secret",
    })).rejects.toBeInstanceOf(ApiTransportError);
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "tapd", secret_ref: "env:TAPD_TOKEN",
    })).resolves.toMatchObject({ id: "profile-2" });
  });
});
