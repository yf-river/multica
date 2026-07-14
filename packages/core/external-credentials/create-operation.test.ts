// @vitest-environment jsdom

import { webcrypto } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ApiTransportError } from "../api";
import { resetAccountState } from "../platform/workspace-storage";
import type { ExternalCredentialProfile } from "../types";
import { createExternalCredentialProfileWithRecovery } from "./create-operation";

const profile = (id: string) => ({ id }) as ExternalCredentialProfile;

describe("createExternalCredentialProfileWithRecovery", () => {
  beforeEach(() => {
    vi.stubGlobal("crypto", webcrypto);
    localStorage.clear();
    resetAccountState();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("persists only a request fingerprint when a token outcome is unknown", async () => {
    const createExternalCredentialProfile = vi.fn().mockRejectedValue(
      new ApiTransportError("POST credential", true, new Error("lost")),
    );
    const client = {
      createExternalCredentialProfile,
      getExternalCredentialProfile: vi.fn(),
    };
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng",
      name: "Current",
      token: "secret-sentinel-never-persist",
    }, client)).rejects.toBeInstanceOf(ApiTransportError);

    const stored = localStorage.getItem("multica_credential_profile_create") ?? "";
    expect(stored).not.toContain("secret-sentinel-never-persist");
    expect(stored).toMatch(/[0-9a-f]{64}/);
  });

  it("recovers a committed profile before accepting changed secret input", async () => {
    const getExternalCredentialProfile = vi.fn().mockResolvedValue(profile("profile-1"));
    const createExternalCredentialProfile = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old credential", true, new Error("lost")))
      .mockResolvedValueOnce(profile("profile-2"));
    const client = {
      getExternalCredentialProfile,
      createExternalCredentialProfile,
    };

    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng", token: "old-secret",
    }, client)).rejects.toBeInstanceOf(ApiTransportError);
    const oldRequestKey = createExternalCredentialProfile.mock.calls[0]?.[1];
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng", token: "new-secret",
    }, client)).resolves.toMatchObject({ id: "profile-2" });
    expect(getExternalCredentialProfile).toHaveBeenCalledWith(oldRequestKey);
    expect(createExternalCredentialProfile).toHaveBeenCalledTimes(2);
  });

  it("creates the current request when the older request did not commit", async () => {
    const client = {
      getExternalCredentialProfile: vi.fn().mockRejectedValue(new ApiError("missing", 404, "Not Found")),
      createExternalCredentialProfile: vi.fn()
        .mockRejectedValueOnce(new ApiTransportError("POST missing credential", true, new Error("lost")))
        .mockResolvedValueOnce(profile("profile-2")),
    };
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng", token: "old-secret",
    }, client)).rejects.toBeInstanceOf(ApiTransportError);
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "tapd", secret_ref: "env:TAPD_TOKEN",
    }, client)).resolves.toMatchObject({ id: "profile-2" });
  });
});
