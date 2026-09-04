"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { KeyRound, LoaderCircle, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  externalCredentialProfilesOptions,
  useCreateExternalCredentialProfile,
  useDeleteExternalCredentialProfile,
  useTestExternalCredentialProfile,
  useUpdateExternalCredentialProfile,
} from "@multica/core/external-credentials";
import type { ExternalCredentialProfile, ExternalCredentialProvider } from "@multica/core/types/external-credential";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { useT } from "../../i18n";

const PROVIDERS: ExternalCredentialProvider[] = ["tapd", "gongfeng"];

export function ExternalCredentialPanel() {
  return (
    <div className="space-y-4">
      {PROVIDERS.map((provider) => <ProviderCard key={provider} provider={provider} />)}
    </div>
  );
}

function ProviderCard({ provider }: { provider: ExternalCredentialProvider }) {
  const { t } = useT("settings");
  const title = provider === "tapd"
    ? t(($) => $.external_credentials.tapd_title)
    : t(($) => $.external_credentials.gongfeng_title);
  const help = provider === "tapd"
    ? t(($) => $.external_credentials.tapd_help)
    : t(($) => $.external_credentials.gongfeng_help);
  const profiles = useQuery(externalCredentialProfilesOptions(provider));
  const createProfile = useCreateExternalCredentialProfile(provider);
  const updateProfile = useUpdateExternalCredentialProfile(provider);
  const deleteProfile = useDeleteExternalCredentialProfile(provider);
  const testProfile = useTestExternalCredentialProfile();
  const profile = profiles.data?.[0] ?? null;
  const [name, setName] = useState("");
  const [token, setToken] = useState("");

  const busy = createProfile.isPending || updateProfile.isPending || testProfile.isPending;
  const save = async () => {
    if (!token.trim() && !profile) {
      toast.error(t(($) => $.external_credentials.token_required));
      return;
    }
    try {
      if (profile) {
        await updateProfile.mutateAsync({
          id: profile.id,
          request: {
            name: name.trim() || profile.name,
            ...(token.trim() ? { token: token.trim() } : {}),
            status: "unverified",
          },
        });
      } else {
        await createProfile.mutateAsync({
          provider,
          name: name.trim() || title,
          token: token.trim(),
          verify_now: true,
        });
      }
      setToken("");
      toast.success(t(($) => $.external_credentials.saved, { title }));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.external_credentials.save_failed));
    }
  };

  const test = async () => {
    try {
      const result = token.trim()
        ? await testProfile.mutateAsync({ provider, token: token.trim() })
        : profile
          ? await updateProfile.mutateAsync({ id: profile.id, request: { verify_now: true } })
          : null;
      if (!result) return;
      if (result.status === "verified") {
        toast.success(t(($) => $.external_credentials.test_succeeded));
      } else {
        toast.error(result.last_error || t(($) => $.external_credentials.test_failed));
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.external_credentials.test_failed));
    }
  };

  const remove = async (value: ExternalCredentialProfile) => {
    if (!window.confirm(t(($) => $.external_credentials.delete_confirm, { name: value.name }))) return;
    await deleteProfile.mutateAsync(value);
    toast.success(t(($) => $.external_credentials.deleted));
  };

  return (
    <Card>
      <CardContent className="space-y-4">
        <div className="flex items-start justify-between gap-4">
          <div className="flex gap-3">
            <KeyRound className="mt-0.5 h-4 w-4 text-muted-foreground" />
            <div>
              <div className="text-body font-medium">{title}</div>
              <p className="mt-1 text-caption text-muted-foreground">{help}</p>
            </div>
          </div>
          {profile && <Badge variant="outline">{statusLabel(profile.status, t)}</Badge>}
        </div>
        {profiles.isLoading ? (
          <div className="flex items-center gap-2 text-caption text-muted-foreground">
            <LoaderCircle className="h-4 w-4 animate-spin" />
            {t(($) => $.external_credentials.loading)}
          </div>
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder={profile?.name || t(($) => $.external_credentials.name_placeholder)} />
            <Input type="password" value={token} onChange={(event) => setToken(event.target.value)} placeholder={profile?.secret_binding.configured ? t(($) => $.external_credentials.token_saved_placeholder) : t(($) => $.external_credentials.token_placeholder)} autoComplete="new-password" />
          </div>
        )}
        {profile?.last_error && <p className="text-caption text-destructive">{profile.last_error}</p>}
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={save} disabled={busy || profiles.isLoading}>{t(($) => $.external_credentials.save)}</Button>
          <Button size="sm" variant="outline" onClick={test} disabled={busy || (!profile && !token.trim())}>{t(($) => $.external_credentials.test)}</Button>
          {profile && <Button size="sm" variant="ghost" className="text-destructive" onClick={() => remove(profile)} disabled={deleteProfile.isPending}><Trash2 className="h-4 w-4" />{t(($) => $.external_credentials.delete)}</Button>}
        </div>
      </CardContent>
    </Card>
  );
}

type Translator = ReturnType<typeof useT<"settings">>["t"];

function statusLabel(status: string, t: Translator) {
  switch (status) {
    case "verified": return t(($) => $.external_credentials.status_verified);
    case "failed": return t(($) => $.external_credentials.status_failed);
    case "disabled": return t(($) => $.external_credentials.status_disabled);
    default: return t(($) => $.external_credentials.status_unverified);
  }
}
