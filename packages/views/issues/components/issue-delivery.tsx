"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { deliveryKeys, deliveryOptions } from "@multica/core/delivery/queries";
import { Button } from "@multica/ui/components/ui/button";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

type Confirmation = { kind: "plan" | "production" | "retest"; id: string; revision: number; commit?: string };

export function IssueDelivery({ issueId }: { issueId: string }) {
  const wsId = useWorkspaceId();
  const { t } = useT("issues");
  const client = useQueryClient();
  const snapshot = useQuery(deliveryOptions(wsId, issueId));
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const act = useMutation({
    mutationFn: (selection: Confirmation) => selection.kind === "plan"
      ? api.confirmIssuePlan(issueId, selection.id, selection.revision)
      : api.actIssueDelivery(issueId, selection.id, selection.revision, selection.kind === "production" ? "approve" : "retest"),
    onSuccess: (data) => { client.setQueryData(deliveryKeys.detail(wsId, issueId), data); setConfirmation(null); },
    onError: (e) => { toast.error(e instanceof Error ? e.message : String(e)); void snapshot.refetch(); },
  });
  const data = snapshot.data;
  if (snapshot.isError) return <p role="alert" className="text-caption text-destructive">{t(($) => $.delivery.load_failed)} <Button variant="link" onClick={() => void snapshot.refetch()}>{t(($) => $.delivery.retry)}</Button></p>;
  if (!data || (!data.plan && data.deliveries.length === 0)) return null;
  const plan = data.plan;
  const latest = data.deliveries[0];
  const current = confirmation?.kind === "plan"
    ? Boolean(plan && plan.id === confirmation.id && plan.comment_revision === confirmation.revision && !plan.stale)
    : Boolean(latest && latest.id === confirmation?.id && latest.revision === confirmation?.revision && latest.phase === "awaiting_review");
  return <section aria-label={t(($) => $.delivery.title)} className="min-w-0 space-y-3 rounded-lg border border-surface-border p-3">
    <h3 className="text-body font-medium">{t(($) => $.delivery.title)}</h3>
    {plan && <div className="space-y-2">
      <p className="text-caption text-muted-foreground">{plan.stale ? t(($) => $.delivery.plan_stale) : plan.confirmed_by ? t(($) => $.delivery.plan_confirmed) : t(($) => $.delivery.plan_pending)}</p>
      <details><summary className="cursor-pointer text-caption underline underline-offset-4">{t(($) => $.delivery.view_plan)}</summary><div className="mt-2 max-h-80 overflow-y-auto whitespace-pre-wrap break-words text-caption">{plan.body}</div></details>
      {!plan.confirmed_by && data.can_confirm && <Button size="sm" disabled={plan.stale || act.isPending} onClick={() => setConfirmation({ kind: "plan", id: plan.id, revision: plan.comment_revision })}>{t(($) => $.delivery.confirm_plan)}</Button>}
    </div>}
    {data.authorizations?.length > 0 && <details>
      <summary className="cursor-pointer text-caption underline underline-offset-4">{t(($) => $.delivery.authorization_basis)}</summary>
      <ul className="mt-2 space-y-3 text-caption">{data.authorizations.map((authorization) => <li key={authorization.id}>
        <p className="font-medium">{authorization.kind === "release" ? t(($) => $.delivery.authorization_release) : authorization.kind === "revoke" ? t(($) => $.delivery.authorization_revoke) : t(($) => $.delivery.authorization_execute)}{authorization.revoked ? ` · ${t(($) => $.delivery.authorization_revoked)}` : ""}</p>
        <blockquote className="mt-1 whitespace-pre-wrap break-words border-l-2 border-surface-border pl-2 text-muted-foreground">{authorization.comment_snapshot.content}</blockquote>
        <p className="mt-1 whitespace-pre-wrap break-words">{authorization.scope}</p>
      </li>)}</ul>
    </details>}
    {latest && <div className="space-y-2 border-t border-surface-border pt-3">
      <p className="text-body font-medium" role="status">{t(($) => $.delivery.phases[latest.phase])}</p>
      <p className="break-all font-mono text-caption text-muted-foreground">{latest.source_commit.slice(0, 12)}</p>
      {latest.queue_ahead > 0 && <p className="text-caption text-muted-foreground">{t(($) => $.delivery.queue, { count: latest.queue_ahead })}</p>}
      {latest.error && <p className="whitespace-pre-wrap break-words text-caption text-destructive">{latest.error}</p>}
      {latest.candidate_url && <p className="text-caption text-muted-foreground">{t(($) => $.delivery.tested_version)} {latest.candidate_id}. {t(($) => $.delivery.environment_hint)}</p>}
      {latest.candidate_url && (latest.phase === "testing" || latest.phase === "preparing_release") && <a className="inline-block text-caption underline underline-offset-4" href={latest.candidate_url} target="_blank" rel="noreferrer">{t(($) => $.delivery.open_int)}</a>}
      <details><summary className="cursor-pointer text-caption underline underline-offset-4">{t(($) => $.delivery.tests)}</summary><ol className="mt-2 list-decimal space-y-2 pl-5 text-caption">{latest.evidence.map((check, index) => <li key={index}><span className="font-medium">{check.name}</span><p className="whitespace-pre-wrap break-words text-muted-foreground">{check.evidence}</p></li>)}</ol></details>
      {latest.phase === "awaiting_review" && data.can_confirm && <Button size="sm" disabled={act.isPending} onClick={() => setConfirmation({ kind: "production", id: latest.id, revision: latest.revision, commit: latest.source_commit })}>{t(($) => $.delivery.approve_production)}</Button>}
      {latest.phase === "awaiting_review" && data.can_confirm && <Button variant="outline" size="sm" className="ml-2" disabled={act.isPending} onClick={() => setConfirmation({ kind: "retest", id: latest.id, revision: latest.revision })}>{t(($) => $.delivery.retest)}</Button>}
      {latest.phase === "published" && <p className="text-caption text-muted-foreground">{t(($) => $.delivery.published_hint)}</p>}
    </div>}
    {data.deliveries.length > 1 && <details><summary className="cursor-pointer text-caption text-muted-foreground">{t(($) => $.delivery.history)}</summary><ul className="mt-2 space-y-2 text-caption">{data.deliveries.slice(1).map((d) => <li key={d.id}>{d.source_commit.slice(0, 12)} · {t(($) => $.delivery.phases[d.phase])}</li>)}</ul></details>}
    <AlertDialog open={confirmation !== null} onOpenChange={(open) => { if (!open && !act.isPending) setConfirmation(null); }}>
      <AlertDialogContent>
        <AlertDialogHeader><AlertDialogTitle>{confirmation?.kind === "plan" ? t(($) => $.delivery.confirm_plan) : confirmation?.kind === "retest" ? t(($) => $.delivery.retest) : t(($) => $.delivery.approve_production)}</AlertDialogTitle><AlertDialogDescription>{confirmation?.kind === "plan" ? t(($) => $.delivery.plan_confirmation_hint) : confirmation?.kind === "retest" ? t(($) => $.delivery.retest_hint) : t(($) => $.delivery.production_confirmation_hint, { commit: confirmation?.commit?.slice(0, 12) ?? "" })}</AlertDialogDescription></AlertDialogHeader>
        {!current && <p role="alert" className="text-caption text-destructive">{t(($) => $.delivery.changed)}</p>}
        <AlertDialogFooter><AlertDialogCancel disabled={act.isPending}>{t(($) => $.delivery.cancel)}</AlertDialogCancel><AlertDialogAction disabled={!current || act.isPending} onClick={() => { if (confirmation) act.mutate(confirmation); }}>{act.isPending ? t(($) => $.delivery.submitting) : t(($) => $.delivery.confirm)}</AlertDialogAction></AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </section>;
}
