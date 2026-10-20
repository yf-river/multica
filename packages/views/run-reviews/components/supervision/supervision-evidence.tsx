"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { supervisionKeys } from "@multica/core/supervision/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@multica/ui/components/ui/dialog";
import { AppLink, useNavigation } from "../../../navigation";
import { useT } from "../../../i18n";
import { errorText } from "./supervision-shared";

export type EvidenceTarget = { kind: string; id: string; batch?: string; run?: string };

type ViewerProps = EvidenceTarget & { showHeading?: boolean };

/**
 * Read-only evidence viewer. Shared by the records tab strip and the in-place
 * dialog so both render identical snapshot/current wording and paging rules.
 */
export function EvidenceViewer({ kind, id, batch, showHeading = true }: ViewerProps) {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const [offset, setOffset] = useState(0);
  const evidence = useQuery({
    queryKey: [...supervisionKeys.all(wsId), "evidence", kind, id, batch ?? "", offset],
    queryFn: () => api.getSupervisionEvidence(kind, id, offset, batch),
    enabled: Boolean(kind && id),
  });

  return (
    <>
      {showHeading && <h2 className="text-body font-medium">{t(($) => $.supervision.evidence_panel)}</h2>}
      <p className="mt-1 break-all font-mono text-caption text-muted-foreground">
        {kind} · {id}
        {evidence.data?.run_id && ` · ${evidence.data.run_id}`}
      </p>
      {evidence.isPending ? (
        <p role="status" className="mt-3">{t(($) => $.supervision.loading)}</p>
      ) : evidence.isError ? (
        <div role="alert" className="mt-3">
          <p>{t(($) => $.supervision.evidence_unavailable)}</p>
          <p className="text-caption">{errorText(evidence.error)}</p>
        </div>
      ) : evidence.data?.deleted ? (
        <p role="status" className="mt-3">{t(($) => $.supervision.evidence_unavailable)}</p>
      ) : (
        <>
          <p className="mt-3 text-caption text-muted-foreground">
            {evidence.data?.snapshot === true
              ? t(($) => $.supervision.evidence_snapshot)
              : kind !== "investigation" ? t(($) => $.supervision.evidence_current) : null}
          </p>
          <pre className="mt-3 whitespace-pre-wrap break-all text-caption">{evidence.data?.text}</pre>
          <div className="mt-3 flex flex-wrap items-center gap-2 text-caption tabular-nums">
            <span>{evidence.data?.offset ?? offset}–{evidence.data?.end} / {evidence.data?.length}</span>
            <Button variant="outline" size="sm" disabled={evidence.data?.snapshot === true || offset === 0} onClick={() => setOffset(Math.max(0, offset - 8000))}>
              {t(($) => $.supervision.previous)}
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={!evidence.data || evidence.data.snapshot === true || evidence.data.end >= evidence.data.length}
              onClick={() => setOffset(evidence.data?.end ?? offset)}
            >
              {t(($) => $.supervision.next)}
            </Button>
          </div>
        </>
      )}
    </>
  );
}

/**
 * In-place evidence dialog used by the supervision detail sheet. Opening it
 * never changes the route, so the review detail and the finding stay visible.
 */
export function SupervisionEvidenceDialog({ target, recordsHref, close }: { target: EvidenceTarget | null; recordsHref: string; close: () => void }) {
  const { t } = useT("run-reviews");
  return (
    <Dialog open={Boolean(target)} onOpenChange={(open) => { if (!open) close(); }}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl">
        <DialogTitle>{t(($) => $.supervision.evidence_panel)}</DialogTitle>
        <DialogDescription className="sr-only">{t(($) => $.supervision.detail_hint)}</DialogDescription>
        {target && (
          <EvidenceViewer
            key={`${target.kind}:${target.id}:${target.batch ?? ""}`}
            kind={target.kind}
            id={target.id}
            batch={target.batch}
            showHeading={false}
          />
        )}
        <div className="flex justify-end">
          <Button variant="outline" size="sm" render={<AppLink href={recordsHref} />}>
            {t(($) => $.supervision.open_in_records)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** Evidence strip shown above the records tab when a citation is opened there. */
export function SupervisionEvidencePanel() {
  const { t } = useT("run-reviews");
  const nav = useNavigation();
  const kind = nav.searchParams.get("evidence_kind") ?? "";
  const id = nav.searchParams.get("evidence_id") ?? "";
  const batch = nav.searchParams.get("evidence_batch") ?? "";
  if (!kind || !id) return null;
  return (
    <section
      aria-label={t(($) => $.supervision.evidence_panel)}
      className="mx-4 my-3 max-h-[45vh] shrink-0 overflow-y-auto rounded-lg border border-info/40 bg-info/5 p-3 sm:mx-6"
    >
      <EvidenceViewer key={`${kind}:${id}:${batch}`} kind={kind} id={id} batch={batch} />
    </section>
  );
}
