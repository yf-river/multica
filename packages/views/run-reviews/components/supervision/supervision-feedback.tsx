"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, ApiError } from "@multica/core/api";
import { createSafeId } from "@multica/core/utils";
import { useWorkspacePaths } from "@multica/core/paths";
import type { SupervisionFinding } from "@multica/core/supervision/schema";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@multica/ui/components/ui/dialog";
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
 * Feedback composer. Sending always goes through an explicit trigger preview so
 * the author can see who will be notified, and the receipt distinguishes
 * "comment saved" from "owner actually picked it up".
 */
export function SupervisionFeedbackDialog({ open, close, finding, issueId, assigneeType, assigneeId }: Props) {
  const { t } = useT("run-reviews");
  const { t: issuesT } = useT("issues");
  const paths = useWorkspacePaths();
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
  const blocked = preview.data?.blocked ?? [];
  return (
    <Dialog open={open} onOpenChange={(value) => { if (!value && !send.isPending) close(); }}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogTitle>{t(($) => $.supervision.feedback)}</DialogTitle>
        <DialogDescription>{t(($) => $.supervision.feedback_hint)}</DialogDescription>
        <p className="text-caption text-muted-foreground">{t(($) => $.supervision.feedback_steps)}</p>
        <label className="space-y-2 text-body" htmlFor="supervision-feedback-content">
          <span>{t(($) => $.supervision.feedback_content)}</span>
          <textarea
            id="supervision-feedback-content"
            className="min-h-52 w-full rounded-md border bg-background p-3"
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
        </label>
        {preview.data && previewedContent === content && (
          <div className="space-y-1 text-caption">
            <p>{t(($) => $.supervision.feedback_targets, { count: preview.data.agents.length })}</p>
            <ul className="space-y-0.5">{preview.data.agents.map((agent) => <li key={agent.id}>{agent.name}</li>)}</ul>
            {blocked.length > 0 && (
              <div className="space-y-0.5 text-warning">
                <p>{t(($) => $.supervision.trigger_blocked)}</p>
                <ul className="space-y-0.5">{blocked.map((item) => <li key={`${item.target_type}:${item.target_id}`}>{blockedReasonLabel(item.reason_code, issuesT)}</li>)}</ul>
              </div>
            )}
          </div>
        )}
        {send.data && (
          <div role="status" className="space-y-1 text-body">
            <p>{send.data.pending ? t(($) => $.supervision.feedback_pending) : send.data.reconciled ? t(($) => $.supervision.feedback_unknown) : t(($) => $.supervision.feedback_sent)}</p>
            {!send.data.pending && !send.data.reconciled && <p className="text-caption text-muted-foreground">{t(($) => $.supervision.feedback_receipt)}</p>}
            {send.data.trigger_outcomes?.map((outcome) => (
              <p key={outcome.target_id} className="text-caption">
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
        {!previewReady && !send.data && (
          <p className="text-caption text-muted-foreground">{stale ? t(($) => $.supervision.preview_stale) : t(($) => $.supervision.send_needs_preview)}</p>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="ghost" disabled={send.isPending} onClick={close}>{t(($) => $.supervision.cancel)}</Button>
          <Button variant="outline" disabled={preview.isPending || send.isPending || send.isSuccess} onClick={() => preview.mutate()}>
            {t(($) => $.supervision.preview_feedback)}
          </Button>
          <Button
            disabled={!content.trim() || !previewReady || send.isPending || (send.isSuccess && !send.data?.pending)}
            onClick={() => send.mutate()}
          >
            {send.data?.pending ? t(($) => $.supervision.query_receipt) : t(($) => $.supervision.send_feedback)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
