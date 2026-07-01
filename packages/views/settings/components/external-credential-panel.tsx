"use client";

import { useState, type FormEvent } from "react";
import { CheckCircle2, CircleAlert, KeyRound, Save, ShieldCheck } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { cn } from "@multica/ui/lib/utils";
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

  const handleVerifySavedCredential = () => {
    if (!profile || pending) return;
    setTestResult(null);
    updateProfile.mutate(
      { id: profile.id, data: { verify_now: true } },
      {
        onSuccess: (saved) => {
          if (saved.status === "failed") {
            toast.error(saved.last_error || config.testErrorToast);
          } else {
            toast.success(config.testSuccessToast);
          }
        },
        onError: (err) => toast.error(err instanceof Error ? err.message : config.testErrorToast),
      },
    );
  };

  const bindingHint = profile?.secret_binding?.hint;
  const bindingMode = profile?.secret_binding?.mode;
  const profileError = profile?.last_error;

  return (
    <Card data-testid={config.testId}>
      <CardContent className="space-y-4">
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_11rem]">
          <div className="min-w-0 space-y-3">
            <div className="flex items-start gap-3">
              <div className="mt-0.5 rounded-md border bg-muted/50 p-2 text-muted-foreground">
                <KeyRound className="size-4" />
              </div>
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="text-sm font-semibold">{config.title}</h2>
                  <CredentialStatus profile={profile} configured={configured} />
                </div>
                <p className="max-w-3xl text-sm text-muted-foreground">{config.description}</p>
              </div>
            </div>
            <div className="grid gap-2 rounded-md border bg-muted/20 p-3 text-xs sm:grid-cols-3">
              <CredentialInfoItem
                label={t(($) => $.tokens.external_credential.info_injected_vars)}
                value={config.defaultEnvName}
              />
              <CredentialInfoItem
                label={t(($) => $.tokens.external_credential.info_binding)}
                value={bindingHint || t(($) => $.tokens.external_credential.info_binding_empty)}
                mono={Boolean(bindingHint)}
              />
              <CredentialInfoItem
                label={t(($) => $.tokens.external_credential.info_mode)}
                value={
                  bindingMode === "secret_ref"
                    ? t(($) => $.tokens.external_credential.mode_secret_ref)
                    : bindingMode === "encrypted_secret"
                      ? t(($) => $.tokens.external_credential.mode_token)
                      : t(($) => $.tokens.external_credential.status_unset)
                }
              />
            </div>
          </div>

          <div className="flex flex-col gap-2 lg:items-stretch" data-testid={`${config.testId}-actions`}>
            {savedCredential && !editing && (
              <>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="w-full whitespace-nowrap"
                  disabled={pending}
                  onClick={handleVerifySavedCredential}
                >
                  {updateProfile.isPending
                    ? t(($) => $.tokens.external_credential.testing)
                    : t(($) => $.tokens.external_credential.test)}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="w-full whitespace-nowrap"
                  disabled={pending}
                  onClick={() => setEditing(true)}
                >
                  {t(($) => $.tokens.external_credential.change)}
                </Button>
              </>
            )}
            {profile && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="w-full whitespace-nowrap text-destructive hover:text-destructive"
                disabled={pending}
                onClick={() => {
                  deleteProfile.mutate(profile, {
                    onSuccess: () => toast.success(config.removedToast),
                    onError: (err) => toast.error(err instanceof Error ? err.message : config.removeErrorToast),
                  });
                }}
              >
                {t(($) => $.tokens.external_credential.remove)}
              </Button>
            )}
          </div>
        </div>

        {showForm && (
          <form className="space-y-3 rounded-md border bg-background p-3" onSubmit={handleSubmit}>
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
              <div className="grid grid-cols-2 gap-2 sm:flex">
                <Button
                  type="button"
                  variant="outline"
                  className="h-10 whitespace-nowrap"
                  disabled={pending}
                  onClick={handleTestConnection}
                >
                  {testProfile.isPending
                    ? t(($) => $.tokens.external_credential.testing)
                    : t(($) => $.tokens.external_credential.test)}
                </Button>
                <Button
                  type="submit"
                  className="h-10 gap-1 whitespace-nowrap"
                  disabled={pending}
                >
                  <Save className="size-3.5" />
                  {t(($) => $.tokens.external_credential.save)}
                </Button>
              </div>
            </div>
          </form>
        )}

        {testResult && (
          <CredentialMessage failed={testResult.status === "failed"}>
            {testResult.last_error || t(($) => $.tokens.external_credential.test_passed)}
          </CredentialMessage>
        )}
        {profileError && (
          <CredentialMessage failed={profile?.status === "failed"}>
            {profileError}
          </CredentialMessage>
        )}
      </CardContent>
    </Card>
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
      : configured
        ? "border-emerald-200 bg-emerald-50 text-emerald-700"
        : "border bg-background text-muted-foreground";
  const Icon = profile.status === "failed" ? CircleAlert : configured ? CheckCircle2 : ShieldCheck;
  return (
    <span className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-normal ${tone}`}>
      <Icon className="size-3" />
      {label}
      {hint ? ` · ${hint}` : ""}
    </span>
  );
}

function CredentialInfoItem({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0 space-y-1">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className={cn("truncate text-xs text-foreground", mono && "font-mono")}>
        {value}
      </div>
    </div>
  );
}

function CredentialMessage({
  failed,
  children,
}: {
  failed?: boolean;
  children: string;
}) {
  return (
    <div
      className={cn(
        "rounded-md border px-3 py-2 text-xs",
        failed
          ? "border-red-200 bg-red-50 text-red-700"
          : "bg-muted/20 text-muted-foreground",
      )}
    >
      {children}
    </div>
  );
}
