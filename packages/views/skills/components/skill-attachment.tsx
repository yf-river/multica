"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import type { SkillFile } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Download } from "lucide-react";
import { useT } from "../../i18n";

type Attachment = Pick<SkillFile, "id" | "path" | "content_type" | "size_bytes" | "sha256">;

export function SkillAttachment({ skillId, file, canEdit, uploading, onReplace }: {
  skillId: string; file: Attachment; canEdit: boolean; uploading: boolean; onReplace: (file: File) => void;
}) {
  const { t } = useT("skills");
  const wsId = useWorkspaceId();
  const image = ["image/png", "image/jpeg", "image/gif", "image/webp"].includes(file.content_type ?? "");
  const query = useQuery({
    queryKey: ["skill-attachment", wsId, skillId, file.id, file.sha256],
    queryFn: () => api.getSkillFileBlob(skillId, file.id),
    enabled: image,
    staleTime: Infinity,
    gcTime: 60_000,
    retry: false,
  });
  const [url, setUrl] = useState("");
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!query.data) {setUrl(""); return;}
    const next = URL.createObjectURL(query.data); setUrl(next);
    return () => URL.revokeObjectURL(next);
  }, [query.data]);
  const download = async () => {
    setDownloading(true); setError("");
    try {
      const data = query.data ?? await api.getSkillFileBlob(skillId,file.id);
      const href = URL.createObjectURL(data);
      const anchor = document.createElement("a"); anchor.href=href;anchor.download=file.path.split("/").pop() ?? file.path;
      anchor.click(); setTimeout(() => URL.revokeObjectURL(href), 1000);
    } catch(e) {setError(e instanceof Error ? e.message : t(($) => $.attachments.failed));}
    finally {setDownloading(false);}
  };
  return <div className="flex h-full min-h-80 flex-col gap-4 overflow-auto p-4 sm:p-6">
    <div className="flex flex-wrap items-center gap-3">
      <p className="min-w-0 flex-1 break-all text-caption text-muted-foreground">{file.content_type} · {((file.size_bytes ?? 0)/1024).toFixed(1)} KB</p>
      <Button variant="outline" className="min-h-11" disabled={downloading} onClick={download}><Download aria-hidden="true" />{t(($) => $.attachments.download)}</Button>
      {canEdit && <label className="flex min-h-11 cursor-pointer items-center rounded-md border px-3 text-caption focus-within:ring-2 focus-within:ring-ring">
        {uploading ? t(($) => $.attachments.uploading) : t(($) => $.attachments.replace)}
        <input type="file" className="sr-only" aria-label={t(($) => $.attachments.replace)} disabled={uploading} onChange={(e) => {const f=e.target.files?.[0];if(f)onReplace(f);e.target.value="";}} />
      </label>}
    </div>
    {(error || query.error) && <p role="alert" className="text-body text-destructive">{error || t(($) => $.attachments.failed)}</p>}
    {image ? url ? <img src={url} alt={file.path} className="max-h-[70vh] max-w-full self-center rounded-md border border-surface-border object-contain" /> : query.isPending && <p role="status">{t(($) => $.attachments.loading)}</p> : <p className="text-body text-muted-foreground">{t(($) => $.attachments.no_preview)}</p>}
  </div>;
}
