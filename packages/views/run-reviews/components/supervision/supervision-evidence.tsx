"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { supervisionKeys } from "@multica/core/supervision/queries";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { AppLink, useNavigation } from "../../../navigation";
import { useT } from "../../../i18n";
import { errorText } from "./supervision-shared";

export type EvidenceTarget = { kind: string; id: string; batch?: string; run?: string };

/** Paged evidence query shared by the in-place dialog and the records-tab strip. */
export function useEvidencePage({ kind, id, batch }: EvidenceTarget) {
  const wsId = useWorkspaceId();
  const [offset, setOffset] = useState(0);
  const query = useQuery({
    queryKey: [...supervisionKeys.all(wsId), "evidence", kind, id, batch ?? "", offset],
    queryFn: () => api.getSupervisionEvidence(kind, id, offset, batch),
    enabled: Boolean(kind && id),
  });
  return { query, offset, setOffset };
}

function EvidenceBody({ kind, page }: { kind: string; page: ReturnType<typeof useEvidencePage> }) {
  const { t } = useT("run-reviews");
  const { query, offset, setOffset } = page;
  if (query.isPending) return <p role="status" className="text-body text-muted-foreground">{t(($) => $.supervision.loading)}</p>;
  if (query.isError) {
    return (
      <div role="alert" className="space-y-1">
        <p className="text-body">{t(($) => $.supervision.evidence_unavailable)}</p>
        <p className="text-caption text-muted-foreground">{errorText(query.error)}</p>
      </div>
    );
  }
  if (query.data?.deleted) return <p role="status" className="text-body text-muted-foreground">{t(($) => $.supervision.evidence_unavailable)}</p>;
  return (
    <>
      <p className="text-caption text-muted-foreground">
        {query.data?.snapshot === true
          ? t(($) => $.supervision.evidence_snapshot)
          : kind !== "investigation" ? t(($) => $.supervision.evidence_current) : null}
      </p>
      <div className="mt-2 rounded-lg border border-surface-border bg-surface-hover">
        <pre className="max-h-[42dvh] overflow-y-auto whitespace-pre-wrap break-all p-4 font-mono text-caption leading-5">
          {query.data?.text}
        </pre>
      </div>
      <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-caption tabular-nums text-muted-foreground">
        <span>{query.data?.offset ?? offset}–{query.data?.end} / {query.data?.length}</span>
        <span className="flex gap-2">
          <Button variant="outline" size="sm" disabled={query.data?.snapshot === true || offset === 0} onClick={() => setOffset(Math.max(0, offset - 8000))}>
            {t(($) => $.supervision.previous)}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!query.data || query.data.snapshot === true || query.data.end >= query.data.length}
            onClick={() => setOffset(query.data?.end ?? offset)}
          >
            {t(($) => $.supervision.next)}
          </Button>
        </span>
      </div>
    </>
  );
}

/** In-place evidence dialog. Reading evidence never changes the route or the review detail. */
export function SupervisionEvidenceDialog({ target, recordsHref, close }: { target: EvidenceTarget | null; recordsHref: string; close: () => void }) {
  const { t } = useT("run-reviews");
  const page = useEvidencePage(target ?? { kind: "", id: "" });
  const runId = target?.run ?? page.query.data?.run_id;
  return (
    <Dialog open={Boolean(target)} onOpenChange={(open) => { if (!open) close(); }}>
      <DialogContent className="sm:max-w-[720px]">
        <DialogHeader className="pr-8">
          <div className="flex flex-wrap items-center gap-2">
            <DialogTitle className="leading-6">{t(($) => $.supervision.evidence_panel)}</DialogTitle>
            {page.query.data && (
              <Badge variant="outline">
                {page.query.data.snapshot === true ? t(($) => $.supervision.evidence_snapshot_label) : t(($) => $.supervision.evidence_current_label)}
              </Badge>
            )}
          </div>
          <DialogDescription className="break-all font-mono text-caption">
            {target?.kind} · {target?.id}{runId ? " · " + runId : ""}
          </DialogDescription>
        </DialogHeader>
        {target && <EvidenceBody kind={target.kind} page={page} />}
        <DialogFooter className="sm:justify-between">
          <Button variant="link" size="sm" className="px-0" render={<AppLink href={recordsHref} />}>
            {t(($) => $.supervision.open_in_records)}
          </Button>
          <Button variant="outline" onClick={close}>{t(($) => $.supervision.close)}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Evidence strip shown above the records tab when a citation is opened there. */
export function SupervisionEvidencePanel() {
  const { t } = useT("run-reviews");
  const nav = useNavigation();
  const target: EvidenceTarget = {
    kind: nav.searchParams.get("evidence_kind") ?? "",
    id: nav.searchParams.get("evidence_id") ?? "",
    batch: nav.searchParams.get("evidence_batch") ?? "",
  };
  const page = useEvidencePage(target);
  if (!target.kind || !target.id) return null;
  return (
    <section
      aria-label={t(($) => $.supervision.evidence_panel)}
      className="mx-4 my-3 max-h-[45vh] shrink-0 overflow-y-auto rounded-lg border border-info/40 bg-info/5 p-3 sm:mx-6"
    >
      <h2 className="text-body font-medium">{t(($) => $.supervision.evidence_panel)}</h2>
      <p className="mt-1 break-all font-mono text-caption text-muted-foreground">
        {target.kind} · {target.id}{page.query.data?.run_id ? " · " + page.query.data.run_id : ""}
      </p>
      <div className="mt-2">
        <EvidenceBody kind={target.kind} page={page} />
      </div>
    </section>
  );
}
