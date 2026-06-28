"use client";

import { useState, type FormEvent } from "react";
import { KeyRound, Save } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  externalCredentialProfilesOptions,
  useCreateExternalCredentialProfile,
  useDeleteExternalCredentialProfile,
  useTestExternalCredentialProfile,
  useUpdateExternalCredentialProfile,
} from "@multica/core/external-credentials";
import type {
  ExternalCredentialProfile,
  ExternalCredentialProvider,
  TestExternalCredentialProfileResponse,
} from "@multica/core/types";
import { useT } from "../../i18n";

export interface ExternalCredentialPanelConfig {
  provider: ExternalCredentialProvider;
  testId: string;
  title: string;
  description: string;
  defaultName: string;
  mcpServer: string;
  defaultEnvName: string;
  tokenPlaceholder: string;
  replaceTokenPlaceholder: string;
  emptyTokenMessage: string;
  savedToast: string;
  saveErrorToast: string;
  unavailableToast: string;
  removedToast: string;
  removeErrorToast: string;
  testSuccessToast: string;
  testErrorToast: string;
}

export function ExternalCredentialPanel({ config }: { config: ExternalCredentialPanelConfig }) {
  const { t } = useT("settings");
  const { data } = useQuery(externalCredentialProfilesOptions(config.provider));
  const createProfile = useCreateExternalCredentialProfile(config.provider);
  const updateProfile = useUpdateExternalCredentialProfile(config.provider);
  const deleteProfile = useDeleteExternalCredentialProfile(config.provider);
  const testProfile = useTestExternalCredentialProfile();
  const profiles = data?.profiles ?? [];
  const profile =
    profiles.find((item) => item.provider === config.provider && item.secret_binding?.configured) ??
    profiles.find((item) => item.provider === config.provider);
  const configured = Boolean(profile?.secret_binding?.configured);
  const savedCredential = configured && profile?.status !== "failed";
  const [mode, setMode] = useState<"token" | "secret_ref">("token");
  const [token, setToken] = useState("");
  const [secretRef, setSecretRef] = useState(config.defaultEnvName);
  const [editing, setEditing] = useState(false);
  const [testResult, setTestResult] = useState<TestExternalCredentialProfileResponse | null>(null);
  const pending = createProfile.isPending || updateProfile.isPending || deleteProfile.isPending || testProfile.isPending;
  const showForm = !savedCredential || editing;

  const credentialInput = () => {
    const isToken = mode === "token";
    const credentialValue = isToken ? token.trim() : normalizeEnvName(secretRef);
    if (!credentialValue) {
      toast.error(isToken ? config.emptyTokenMessage : "请输入服务端环境变量名");
      return null;
    }
    return isToken ? { token: credentialValue } : { secret_ref: `env:${credentialValue}` };
  };

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const secret = credentialInput();
    if (!secret) return;
    const payload = {
      name: profile?.name || config.defaultName,
      capabilities: { mcp_server: config.mcpServer, source: "settings-tokens" },
      verify_now: true,
      ...secret,
    };
    const options = {
      onSuccess: (saved: ExternalCredentialProfile) => {
        setTestResult(null);
        if (saved.status === "failed") {
          toast.error(saved.last_error || config.unavailableToast);
        } else {
          toast.success(config.savedToast);
        }
        setEditing(saved.status === "failed");
        setToken("");
      },
      onError: (err: unknown) => toast.error(err instanceof Error ? err.message : config.saveErrorToast),
    };
    if (profile) {
      updateProfile.mutate({ id: profile.id, data: payload }, options);
    } else {
      createProfile.mutate({ provider: config.provider, ...payload }, options);
    }
  };

  const handleTestConnection = () => {
    const secret = credentialInput();
    if (!secret) return;
    setTestResult(null);
    testProfile.mutate(
      { provider: config.provider, ...secret },
      {
        onSuccess: (result) => {
          setTestResult(result);
          if (result.status === "failed") {
            toast.error(result.last_error || config.testErrorToast);
          } else {
            toast.success(config.testSuccessToast);
          }
        },
        onError: (err) => toast.error(err instanceof Error ? err.message : config.testErrorToast),
      },
    );
  };

  return (
    <section className="space-y-4" data-testid={config.testId}>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">
            <KeyRound className="size-4 text-muted-foreground" />
            <h2>{config.title}</h2>
            <CredentialStatus profile={profile} configured={configured} />
          </div>
          <p className="max-w-3xl text-xs text-muted-foreground">{config.description}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {savedCredential && !editing && (
            <button
              type="button"
              className="inline-flex h-8 shrink-0 items-center justify-center whitespace-nowrap rounded-md border px-2 text-xs text-foreground hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50"
              disabled={pending}
              onClick={() => setEditing(true)}
            >
              {t(($) => $.tokens.external_credential.change)}
            </button>
          )}
          {profile && (
            <button
              type="button"
              className="inline-flex h-8 shrink-0 items-center justify-center whitespace-nowrap rounded-md border px-2 text-xs text-destructive hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-50"
              disabled={pending}
              onClick={() => {
                deleteProfile.mutate(profile, {
                  onSuccess: () => toast.success(config.removedToast),
                  onError: (err) => toast.error(err instanceof Error ? err.message : config.removeErrorToast),
                });
              }}
            >
              {t(($) => $.tokens.external_credential.remove)}
            </button>
          )}
        </div>
      </div>

      {showForm && (
        <form className="space-y-3" onSubmit={handleSubmit}>
          <div className="inline-flex w-fit rounded-md border bg-background p-0.5">
            <button
              type="button"
              className={`h-8 whitespace-nowrap rounded px-2 text-xs ${mode === "token" ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted"}`}
              onClick={() => {
                setMode("token");
                setTestResult(null);
              }}
            >
              {t(($) => $.tokens.external_credential.mode_token)}
            </button>
            <button
              type="button"
              className={`h-8 whitespace-nowrap rounded px-2 text-xs ${mode === "secret_ref" ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted"}`}
              onClick={() => {
                setMode("secret_ref");
                setTestResult(null);
              }}
            >
              {t(($) => $.tokens.external_credential.mode_secret_ref)}
            </button>
          </div>
          <div className="grid gap-2 sm:grid-cols-[minmax(220px,1fr)_auto]">
            {mode === "token" ? (
              <input
                type="password"
                value={token}
                onChange={(event) => {
                  setToken(event.target.value);
                  setTestResult(null);
                }}
                placeholder={configured ? config.replaceTokenPlaceholder : config.tokenPlaceholder}
                className="h-10 min-w-0 rounded-md border bg-background px-3 text-sm outline-none focus:ring-2 focus:ring-ring"
                autoComplete="off"
              />
            ) : (
              <input
                type="text"
                value={secretRef}
                onChange={(event) => {
                  setSecretRef(event.target.value);
                  setTestResult(null);
                }}
                placeholder={config.defaultEnvName}
                className="h-10 min-w-0 rounded-md border bg-background px-3 font-mono text-sm outline-none focus:ring-2 focus:ring-ring"
              />
            )}
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                className="inline-flex h-10 items-center justify-center whitespace-nowrap rounded-md border px-3 text-sm hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50"
                disabled={pending}
                onClick={handleTestConnection}
              >
                {testProfile.isPending
                  ? t(($) => $.tokens.external_credential.testing)
                  : t(($) => $.tokens.external_credential.test)}
              </button>
              <button
                type="submit"
                className="inline-flex h-10 items-center justify-center gap-1 whitespace-nowrap rounded-md bg-foreground px-3 text-sm text-background hover:bg-foreground/90 disabled:cursor-not-allowed disabled:opacity-50"
                disabled={pending}
              >
                <Save className="size-3.5" />
                {t(($) => $.tokens.external_credential.save)}
              </button>
            </div>
          </div>
          {testResult && (
            <p className={testResult.status === "failed" ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
              {testResult.last_error || "连接测试通过"}
            </p>
          )}
          {profile?.last_error && (
            <p className={profile.status === "failed" ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
              {profile.last_error}
            </p>
          )}
        </form>
      )}
    </section>
  );
}

function normalizeEnvName(value: string): string {
  return value.trim().replace(/^env:/i, "").trim();
}

function CredentialStatus({
  profile,
  configured,
}: {
  profile: ExternalCredentialProfile | undefined;
  configured: boolean;
}) {
  const { t } = useT("settings");
  if (!profile) {
    return (
      <span className="rounded border px-1.5 py-0.5 text-[11px] font-normal text-muted-foreground">
        {t(($) => $.tokens.external_credential.status_unset)}
      </span>
    );
  }
  const hint = profile.secret_binding?.hint;
  const label =
    profile.status === "failed"
      ? t(($) => $.tokens.external_credential.status_failed)
      : configured
        ? t(($) => $.tokens.external_credential.status_configured)
        : t(($) => $.tokens.external_credential.status_unset);
  const tone =
    profile.status === "failed"
      ? "border-red-200 bg-red-50 text-red-700"
      : "border bg-background text-muted-foreground";
  return (
    <span className={`rounded px-1.5 py-0.5 text-[11px] font-normal ${tone}`}>
      {label}
      {hint ? ` · ${hint}` : ""}
    </span>
  );
}
