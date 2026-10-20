"use client";

import { useId, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, ApiError } from "@multica/core/api";
import { createSafeId } from "@multica/core/utils";
import { useWorkspacePaths } from "@multica/core/paths";
import type { SupervisionFinding } from "@multica/core/supervision/schema";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../../i18n";
import { blockedReasonLabel } from "../../../issues/blocked-trigger-copy";
import { errorText } from "./supervision-shared";

type Props = {
  open: boolean;
  close: () => void;
  finding: SupervisionFinding;
  issueId: string;
  assigneeType: string | null;
  assigneeId: string | null;
};

/**
 * Feedback composer. The trigger preview is a persistent state block instead of
 * prose instructions: it always shows who will be notified, or why nobody is
 * known yet, so the send button never needs a paragraph of explanation.
 */
export function SupervisionFeedbackDialog({ open, close, finding, issueId, assigneeType, assigneeId }: Props) {
  const { t } = useT("run-reviews");
  const { t: issuesT } = useT("issues");
  const paths = useWorkspacePaths();
  const contentId = useId();
  const stateId = useId();
  const mention = assigneeId && (assigneeType === "agent" || assigneeType === "squad") ? `[@负责人](mention://${assigneeType}/${assigneeId})\n\n` : "";
  const [content, setContent] = useState(() => {
    const references = finding.evidence.map((e) => {
      const query = new URLSearchParams({ tab: "records", issue: issueId, evidence_kind: e.kind, evidence_id: e.id });
      if (e.run_id) query.set("task", e.run_id);
      if (e.batch_id) query.set("evidence_batch", e.batch_id);
      return `[${e.kind} · ${e.id.slice(0, 8)}](${paths.runReviews()}?${query})`;
    }).join("\n");
    const evidenceLabel = t(($) => $.supervision.feedback_template_evidence);
    const impactLabel = t(($) => $.supervision.feedback_template_impact);
    const expectationLabel = t(($) => $.supervision.feedback_template_expectation);
    return `${mention}${finding.title}\n\n${finding.description}\n\n${evidenceLabel}\n${references}\n\n${impactLabel}${finding.impact}\n\n${expectationLabel}${finding.recommendation}`;
  });
  const [operation, setOperation] = useState(createSafeId);
  const [previewedContent, setPreviewedContent] = useState("");
  const preview = useMutation({ mutationFn: () => api.previewCommentTriggers(issueId, content), onSuccess: () => setPreviewedContent(content), onError: (e) => toast.error(errorText(e)) });
  const send = useMutation({ mutationFn: () => api.sendSupervisionFeedback(finding.id, { content, operation_id: operation }), onError: (e) => toast.error(errorText(e)) });
  const previewReady = previewedContent !== "" && previewedContent === content;
  const stale = previewedContent !== "" && previewedContent !== content;
  const locked = send.isPending || send.isSuccess || (send.isError && !(send.error instanceof ApiError && send.error.status >= 400 && send.error.status < 500));
  const blocked = previewReady ? preview.data?.blocked ?? [] : [];
  const agents = previewReady ? preview.data?.agents ?? [] : [];
  return (
    <Dialog open={open} onOpenChange={(value) => { if (!value && !send.isPending) close(); }}>
      <DialogContent className="sm:max-w-[600px]">
        <DialogHeader className="pr-8">
          <DialogTitle className="leading-6">{t(($) => $.supervision.feedback)}</DialogTitle>
          <DialogDescription>{t(($) => $.supervision.feedback_hint)}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <label htmlFor={contentId} className="block text-caption text-muted-foreground">
              {t(($) => $.supervision.feedback_content)}
            </label>
            <Textarea
              id={contentId}
              className="max-h-[40dvh] min-h-44 resize-y bg-surface-hover/60 p-3 leading-6"
              value={content}
              disabled={locked}
              onChange={(e) => {
                setContent(e.target.value);
                if (send.isError) {
                  send.reset();
                  setOperation(createSafeId());
                }
              }}
            />
          </div>

          <section id={stateId} aria-label={t(($) => $.supervision.trigger_targets)} aria-live="polite" className="rounded-lg bg-surface-hover px-3 py-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-caption text-muted-foreground">{t(($) => $.supervision.trigger_targets)}</span>
              {previewReady ? (
                agents.length > 0 ? agents.map((agent) => (
                  <span key={agent.id} className="inline-flex items-center gap-1.5 rounded-full bg-surface-raised px-2 py-0.5 text-caption text-foreground ring-1 ring-surface-border">
                    <span aria-hidden className="size-1.5 rounded-full bg-info" />
                    {agent.name}
                  </span>
                )) : <span className="text-caption text-muted-foreground">{t(($) => $.supervision.trigger_none)}</span>
              ) : (
                <span className={cn("text-caption", stale ? "text-warning" : "text-muted-foreground")}>
                  {stale ? t(($) => $.supervision.preview_stale) : t(($) => $.supervision.trigger_not_previewed)}
                </span>
              )}
            </div>
            {blocked.length > 0 && (
              <ul className="mt-1.5 space-y-0.5 text-caption text-warning">
                {blocked.map((item) => <li key={item.target_type + ":" + item.target_id}>{blockedReasonLabel(item.reason_code, issuesT)}</li>)}
              </ul>
            )}
          </section>

          {send.data && (
            <div role="status" className="rounded-lg border border-info/30 bg-info/5 p-3">
              <p className="text-body font-medium">
                {send.data.pending ? t(($) => $.supervision.feedback_pending) : send.data.reconciled ? t(($) => $.supervision.feedback_unknown) : t(($) => $.supervision.feedback_sent)}
              </p>
              {!send.data.pending && !send.data.reconciled && <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.supervision.feedback_receipt)}</p>}
              {send.data.trigger_outcomes?.map((outcome) => (
                <p key={outcome.target_id} className="mt-1 text-caption">
                  {outcome.status === "queued" ? t(($) => $.supervision.feedback_queued)
                    : outcome.status === "coalesced" ? t(($) => $.supervision.feedback_coalesced)
                    : outcome.status === "deferred" ? t(($) => $.supervision.feedback_deferred)
                    : outcome.status === "blocked" ? blockedReasonLabel(outcome.reason_code, issuesT)
                    : t(($) => $.supervision.feedback_unknown)}
                </p>
              ))}
            </div>
          )}
          {send.isError && <p role="alert" className="text-destructive">{errorText(send.error)}</p>}
        </div>

        <DialogFooter className="items-center">
          <p className="mr-auto hidden text-caption text-muted-foreground sm:block">
            {previewReady ? t(($) => $.supervision.feedback_route_hint) : t(($) => $.supervision.send_needs_preview)}
          </p>
          <Button variant="ghost" disabled={send.isPending} onClick={close}>{t(($) => $.supervision.cancel)}</Button>
          <Button variant="outline" disabled={preview.isPending || send.isPending || send.isSuccess} onClick={() => preview.mutate()}>
            {t(($) => $.supervision.preview_feedback)}
          </Button>
          <Button
            aria-describedby={!previewReady ? stateId : undefined}
            disabled={!content.trim() || !previewReady || send.isPending || (send.isSuccess && !send.data?.pending)}
            onClick={() => send.mutate()}
          >
            {send.data?.pending ? t(($) => $.supervision.query_receipt) : t(($) => $.supervision.send_feedback)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
