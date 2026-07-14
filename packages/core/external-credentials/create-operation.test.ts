// @vitest-environment jsdom

import { webcrypto } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ApiTransportError } from "../api";
import type { ExternalCredentialProfile } from "../types";
import {
  createExternalCredentialProfileWithRecovery,
  useCredentialProfileCreateStore,
} from "./create-operation";

const profile = (id: string) => ({ id }) as ExternalCredentialProfile;

describe("createExternalCredentialProfileWithRecovery", () => {
  beforeEach(() => {
    vi.stubGlobal("crypto", webcrypto);
    localStorage.clear();
    useCredentialProfileCreateStore.setState({ pending: undefined });
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
    expect(useCredentialProfileCreateStore.getState().pending?.requestFingerprint)
      .toMatch(/^[0-9a-f]{64}$/);
  });

  it("recovers a committed profile before accepting changed secret input", async () => {
    useCredentialProfileCreateStore.getState().setPending({
      requestKey: "10000000-0000-4000-8000-000000000007",
      requestFingerprint: "a".repeat(64),
      createdAt: Date.now(),
    });
    const getExternalCredentialProfile = vi.fn().mockResolvedValue(profile("profile-1"));
    const createExternalCredentialProfile = vi.fn().mockResolvedValue(profile("profile-2"));
    const client = {
      getExternalCredentialProfile,
      createExternalCredentialProfile,
    };

    await expect(createExternalCredentialProfileWithRecovery({
      provider: "gongfeng", token: "new-secret",
    }, client)).resolves.toMatchObject({ id: "profile-2" });
    expect(getExternalCredentialProfile).toHaveBeenCalledWith("10000000-0000-4000-8000-000000000007");
    expect(createExternalCredentialProfile).toHaveBeenCalledTimes(1);
    expect(useCredentialProfileCreateStore.getState().pending).toBeUndefined();
  });

  it("creates the current request when the older request did not commit", async () => {
    useCredentialProfileCreateStore.getState().setPending({
      requestKey: "10000000-0000-4000-8000-000000000008",
      requestFingerprint: "b".repeat(64),
      createdAt: Date.now(),
    });
    const client = {
      getExternalCredentialProfile: vi.fn().mockRejectedValue(new ApiError("missing", 404, "Not Found")),
      createExternalCredentialProfile: vi.fn().mockResolvedValue(profile("profile-2")),
    };
    await expect(createExternalCredentialProfileWithRecovery({
      provider: "tapd", secret_ref: "env:TAPD_TOKEN",
    }, client)).resolves.toMatchObject({ id: "profile-2" });
  });
});
