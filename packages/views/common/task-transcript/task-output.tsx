"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { taskOutputOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import type { TimelineItem } from "./build-timeline";

export function TaskOutput({ item }: { item: TimelineItem }) {
  const { t } = useT("agents");
  const [offset, setOffset] = useState(0);
  const [draft, setDraft] = useState("");
  const [search, setSearch] = useState("");
  const wsId = useWorkspaceId();
  const page = useQuery(taskOutputOptions(wsId, item.taskId ?? "", item.id ?? "", offset, search));
  return (
    <div className="space-y-2 px-3 py-2">
      <form className="flex gap-2" onSubmit={(event) => { event.preventDefault(); setOffset(0); setSearch(draft); }}>
        <Input aria-label={t(($) => $.transcript.output_search)} placeholder={t(($) => $.transcript.output_search)} value={draft} onChange={(event) => setDraft(event.target.value)} />
        <Button type="submit" variant="outline">{t(($) => $.transcript.output_search_action)}</Button>
      </form>
      {page.isPending && <p role="status">{t(($) => $.transcript.output_loading)}</p>}
      {page.isError && <Button variant="outline" onClick={() => void page.refetch()}>{t(($) => $.transcript.output_retry)}</Button>}
      {page.data && <>
        {!page.data.found && <p role="status">{t(($) => $.transcript.output_not_found)}</p>}
        <pre className="whitespace-pre-wrap break-all font-mono text-xs">{page.data.output}</pre>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-micro tabular-nums">{page.data.offset}–{page.data.nextOffset} / {page.data.totalChars}</span>
          <Button variant="outline" disabled={page.data.offset === 0} onClick={() => { setSearch(""); setOffset(Math.max(0, page.data!.offset - 8192)); }}>{t(($) => $.transcript.output_previous)}</Button>
          <Button variant="outline" disabled={!page.data.hasMore} onClick={() => setOffset(page.data!.nextOffset)}>{t(($) => $.transcript.output_next)}</Button>
        </div>
      </>}
    </div>
  );
}
