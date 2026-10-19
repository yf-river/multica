"use client";

import { useState } from "react";
import { FileDiff } from "lucide-react";
import { codeDiffFromResult } from "@multica/core/types/code-diff";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";

export function CodeDiffCard({ result, runLabel }: { result: unknown; runLabel?: string }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState(0);
  const diff = codeDiffFromResult(result);
  if (!diff || (!diff.unavailable && diff.files.length === 0 && !diff.truncated)) return null;
  if (diff.unavailable) return <p className="text-caption text-muted-foreground">本次代码差异采集失败，无法查看历史快照。</p>;
  const file = diff.files[selected] ?? diff.files[0];
  const added = diff.files.reduce((sum, f) => sum + f.added, 0);
  const deleted = diff.files.reduce((sum, f) => sum + f.deleted, 0);
  return <>
    <button type="button" onClick={() => setOpen(true)} className="flex min-h-11 w-full min-w-0 items-center gap-3 rounded-md border bg-card px-3 py-2 text-left text-body hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring">
      <FileDiff className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1">查看本次代码修改 {runLabel && <span className="text-caption text-muted-foreground">{runLabel} · </span>}<span className="text-caption text-muted-foreground">{diff.files.length} 个文件{diff.truncated ? "（部分）" : ""}</span></span>
      <span className="shrink-0 font-mono text-caption tabular-nums"><span className="text-success">+{added}</span> <span className="text-destructive">−{deleted}</span></span>
    </button>
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="flex h-[85vh] w-[calc(100vw-2rem)] min-w-0 flex-col gap-3 overflow-hidden sm:!max-w-6xl">
        <DialogTitle>本次代码修改</DialogTitle>
        <DialogDescription className="break-words text-caption">独立任务目录在运行前后的文件差异，包含已提交和未提交修改。历史快照不会随仓库更新。基线 {diff.before.slice(0, 12)} → {diff.after.slice(0, 12)}。</DialogDescription>
        {diff.truncated && <p role="status" className="text-caption text-muted-foreground">变更较大，部分文件或补丁未展示；行数统计仅覆盖列出的文件。</p>}
        <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-3 md:flex-row">
          <nav aria-label="修改文件" className="max-h-36 shrink-0 overflow-auto md:max-h-none md:w-64">
            {diff.files.map((f, index) => <button key={f.path} type="button" aria-current={index === selected ? "true" : undefined} onClick={() => setSelected(index)} className={cn("flex min-h-11 w-full items-center gap-2 rounded px-2 py-2 text-left text-caption hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring", index === selected && "bg-accent font-semibold")}>
              <span className="min-w-0 flex-1 break-all">{f.path}</span><span className="shrink-0 font-mono tabular-nums">+{f.added} −{f.deleted}</span>
            </button>)}
          </nav>
          <section aria-label="文件差异" className="min-h-0 min-w-0 flex-1 overflow-auto rounded border bg-background">
            {file && <>
              <h3 className="sticky left-0 top-0 break-all border-b bg-muted px-3 py-2 text-caption font-medium">{file.path}</h3>
              {file.binary ? <p className="p-3 text-body text-muted-foreground">二进制文件，无法显示文本差异。</p> : <pre className="w-max min-w-full text-caption leading-5">{file.patch.split("\n").map((line, index) => <span key={index} className={cn("block px-3", line.startsWith("+") && "bg-success/10 text-success", line.startsWith("-") && "bg-destructive/10 text-destructive", line.startsWith("@@") && "bg-muted text-muted-foreground")}>{line || " "}</span>)}</pre>}
              {file.truncated && <p className="p-3 text-caption text-muted-foreground">此文件补丁过大，已截断。</p>}
            </>}
          </section>
        </div>
      </DialogContent>
    </Dialog>
  </>;
}
