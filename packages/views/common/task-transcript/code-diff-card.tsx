"use client";

import { useMemo, useState } from "react";
import { ChevronRight, FileDiff, FolderTree } from "lucide-react";
import {
  codeDiffFromResult,
  codeDiffRoots,
  textChangesFromResult,
  type CodeDiffDir,
  type CodeDiffFile,
  type CodeDiffRoot,
  type RunTextChange,
} from "@multica/core/types/code-diff";
import { Dialog, DialogContent, DialogDescription, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { diffText } from "./text-diff";

function formatBytes(bytes?: number): string {
  if (!bytes) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function changeLabel(file: CodeDiffFile): string {
  switch (file.change) {
    case "added": return "新增";
    case "deleted": return "删除";
    default: return "修改";
  }
}

function Line({ line }: { line: string }) {
  return (
    <span className={cn("block px-3", line.startsWith("+") && "bg-success/10 text-success", line.startsWith("-") && "bg-destructive/10 text-destructive", line.startsWith("@@") && "bg-muted text-muted-foreground")}>
      {line || " "}
    </span>
  );
}

function PatchBody({ file }: { file: CodeDiffFile }) {
  if (file.redacted) {
    return <p className="p-3 text-body text-muted-foreground">疑似敏感文件，已脱敏：只记录路径与大小（{formatBytes(file.size)}），内容未留存。</p>;
  }
  if (file.binary) {
    return <p className="p-3 text-body text-muted-foreground">二进制文件，无法显示文本差异{file.size ? `（${formatBytes(file.size)}）` : ""}。</p>;
  }
  if (!file.patch) {
    const lines = file.lines ?? 0;
    const beforeLines = file.before_lines ?? 0;
    return <p className="p-3 text-body text-muted-foreground">内容未留存，只记录大小：{formatBytes(file.before_size)} → {formatBytes(file.size)}{lines ? `，行数 ${beforeLines} → ${lines}` : ""}。</p>;
  }
  return (
    <>
      <pre className="w-max min-w-full text-caption leading-5">{file.patch.split("\n").map((line, index) => <Line key={index} line={line} />)}</pre>
      {file.truncated && <p className="p-3 text-caption text-muted-foreground">此文件补丁过大，已截断。</p>}
    </>
  );
}

function DirRow({ dir }: { dir: CodeDiffDir }) {
  return (
    <li className="rounded border border-dashed px-2 py-2 text-caption text-muted-foreground">
      <div className="flex items-center gap-2">
        <FolderTree className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="min-w-0 flex-1 break-all">{dir.path}</span>
        <span className="shrink-0 tabular-nums">{dir.files} 个文件 · {formatBytes(dir.size)}</span>
      </div>
      {dir.added && <p className="mt-1 pl-5">新增的依赖目录（只记摘要，不逐文件展开）</p>}
      {dir.removed && <p className="mt-1 pl-5">已删除的依赖目录</p>}
      {!!dir.added_children?.length && <p className="mt-1 pl-5">新增：{dir.added_children.slice(0, 6).join("、")}{dir.added_children.length > 6 ? " 等" : ""}</p>}
      {!!dir.removed_children?.length && <p className="mt-1 pl-5">移除：{dir.removed_children.slice(0, 6).join("、")}{dir.removed_children.length > 6 ? " 等" : ""}</p>}
    </li>
  );
}

function rootSubtitle(root: CodeDiffRoot): string {
  const parts: string[] = [];
  if (root.branch) parts.push(root.branch);
  if (root.commits) parts.push(`${root.commits} 个提交`);
  if (root.base_source === "run-start") parts.push("从运行开始时算起");
  if (root.base_source === "upstream") parts.push("从该分支的上游算起");
  if (root.base_source === "reflog") parts.push("从分支创建点算起");
  if (root.base_source === "merge-base") parts.push("从共同祖先算起");
  if (root.base_source === "unknown") parts.push("基线未知");
  return parts.join(" · ");
}

function TextChangeBody({ change }: { change: RunTextChange }) {
  const lines = useMemo(() => diffText(change.before ?? "", change.after ?? ""), [change.before, change.after]);
  return (
    <li className="rounded border bg-background">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b bg-muted px-3 py-2 text-caption">
        <span className="font-medium">{change.target_name || change.target_id}</span>
        <span className="text-muted-foreground">{change.target_kind} · {change.field}</span>
        <span className="ml-auto font-mono tabular-nums text-success">+{change.added}</span>
        <span className="font-mono tabular-nums text-destructive">−{change.deleted}</span>
      </div>
      {change.redacted
        ? <p className="p-3 text-caption text-muted-foreground">疑似敏感内容，已脱敏：只记录改动行数，内容未留存。</p>
        : <pre className="max-h-64 w-max min-w-full overflow-auto text-caption leading-5">{lines.map((line, index) => <Line key={index} line={`${line.kind}${line.text}`} />)}</pre>}
    </li>
  );
}

export function CodeDiffCard({ result, runLabel, className, variant = "default" }: { result: unknown; runLabel?: string; className?: string; variant?: "default" | "attachment" }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState(0);
  const [activeRoot, setActiveRoot] = useState(0);
  const [showUnassigned, setShowUnassigned] = useState(false);
  const diff = codeDiffFromResult(result);
  const textChanges = textChangesFromResult(result);
  const allRoots = diff ? codeDiffRoots(diff) : [];
  // 1. A worktree nobody could prove belongs to this run is hidden behind a
  //    button: the user still gets to see it, but never as a silent claim that
  //    this task made those edits.
  const unassignedRoots = allRoots.filter((root) => root.attribution === "unassigned");
  const roots = allRoots.filter((root) => root.attribution !== "unassigned" || showUnassigned);
  const files = roots.flatMap((root) => root.files);
  const truncated = diff?.truncated ?? false;
  if (!diff && textChanges.length === 0) return null;
  if (diff?.unavailable && textChanges.length === 0) {
    return <p className={cn("text-caption text-muted-foreground", className)}>{variant === "attachment" ? "本次文件差异采集失败，无法查看历史快照。" : "本次代码差异采集失败，无法查看历史快照。"}{diff.reason ? `（${diff.reason}）` : ""}</p>;
  }
  // 3. An unassigned worktree still counts as content: hiding it must be a
  //    choice the user can undo, not an empty card.
  const anyRootContent = allRoots.some((root) => root.files.length > 0 || (root.dirs?.length ?? 0) > 0);
  if (files.length === 0 && textChanges.length === 0 && !truncated && !anyRootContent) return null;
  const added = files.reduce((sum, file) => sum + file.added, 0);
  const deleted = files.reduce((sum, file) => sum + file.deleted, 0);
  // 2. Comments render the card as a file attachment: the trigger spans the comment
  //    body width and names the file (or a multi-file summary) instead of a run label,
  //    while the diff itself stays in the shared dialog.
  const attachment = variant === "attachment";
  const singleFile = files.length === 1 ? files[0] : undefined;
  const fileName = singleFile?.path.split("/").pop();
  const directory = singleFile?.path.slice(0, singleFile.path.lastIndexOf("/") + 1);
  const title = singleFile ? fileName : files.length > 0 ? `${files.length} 个文件已修改` : `${textChanges.length} 处提示词已修改`;
  const subtitle = singleFile ? directory : files.slice(0, 2).map((file) => file.path.split("/").pop()).join("、") + (files.length > 2 ? " 等" : "");
  const currentRoot = roots[activeRoot] ?? roots[0];
  const currentFiles = currentRoot?.files ?? [];
  const file = currentFiles[selected] ?? currentFiles[0];
  const trigger = attachment ? (
    <div className={className}>
      <DialogTrigger className="flex min-h-14 w-full min-w-0 items-center gap-3 rounded-lg border border-border/60 bg-muted/20 px-3 py-2.5 text-left hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring" aria-label={`查看文件修改：${title}${truncated ? "（部分）" : ""}`}>
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-background text-muted-foreground"><FileDiff className="size-4" aria-hidden="true" /></span>
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-2">
            <span className="truncate text-body font-medium">{title}</span>
            {truncated && <span className="shrink-0 text-caption text-muted-foreground">部分</span>}
          </span>
          {subtitle && <span className="mt-0.5 block truncate text-caption text-muted-foreground">{subtitle}</span>}
        </span>
        <span className="flex shrink-0 items-center gap-2 text-caption tabular-nums">
          {singleFile?.binary ? <span className="text-muted-foreground">二进制文件</span> : <>
            {added > 0 && <span className="font-mono text-success">+{added}</span>}
            {deleted > 0 && <span className="font-mono text-destructive">−{deleted}</span>}
          </>}
          <ChevronRight className="size-4 text-muted-foreground" aria-hidden="true" />
        </span>
      </DialogTrigger>
    </div>
  ) : (
    <DialogTrigger className={cn("flex min-h-11 w-full min-w-0 items-center gap-3 rounded-md border bg-card px-3 py-2 text-left text-body hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring", className)}>
      <FileDiff className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1">查看本次代码修改 {runLabel && <span className="text-caption text-muted-foreground">{runLabel} · </span>}<span className="text-caption text-muted-foreground">{files.length} 个文件{truncated ? "（部分）" : ""}</span></span>
      <span className="shrink-0 font-mono text-caption tabular-nums"><span className="text-success">+{added}</span> <span className="text-destructive">−{deleted}</span></span>
    </DialogTrigger>
  );
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {trigger}
      <DialogContent className="flex h-[85vh] w-[calc(100vw-2rem)] min-w-0 flex-col gap-3 overflow-hidden sm:!max-w-6xl">
        <DialogTitle>{attachment ? "文件修改" : "本次代码修改"}</DialogTitle>
        <DialogDescription className="break-words text-caption">运行前后的文件差异，包含已提交、未提交和被 .gitignore 忽略的文件。任务目录之外的改动单独分组。</DialogDescription>
        {truncated && <p role="status" className="text-caption text-muted-foreground">变更较大，部分文件或补丁未展示；行数统计仅覆盖列出的文件。</p>}
        {!!diff?.notes?.length && <ul role="status" className="flex flex-col gap-1 text-caption text-muted-foreground">{diff.notes.map((note) => <li key={note}>{note}</li>)}</ul>}
        {!!unassignedRoots.length && !showUnassigned && (
          <button type="button" onClick={() => setShowUnassigned(true)} className="self-start rounded-md border px-3 py-1.5 text-caption hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring">
            查看未归属改动（{unassignedRoots.length} 个工作树，{unassignedRoots.reduce((sum, root) => sum + root.files.length, 0)} 个文件）
          </button>
        )}
        <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-3 overflow-auto">
          {textChanges.length > 0 && (
            <section aria-label="平台文本改动" className="shrink-0">
              <h3 className="mb-1 text-caption font-medium">平台文本改动</h3>
              <ul className="flex flex-col gap-2">{textChanges.map((change) => <TextChangeBody key={`${change.target_kind}:${change.target_id}:${change.field}`} change={change} />)}</ul>
            </section>
          )}
          {roots.length > 1 && (
            <nav aria-label="改动分组" className="flex shrink-0 flex-wrap gap-2">
              {roots.map((root, index) => (
                <button key={`${root.path}-${index}`} type="button" aria-current={index === activeRoot ? "true" : undefined}
                  onClick={() => { setActiveRoot(index); setSelected(0); }}
                  className={cn("rounded-md border px-2 py-1 text-left text-caption hover:bg-accent", index === activeRoot && "bg-accent font-semibold")}>
                  <span className="block">{root.label}{root.attribution === "unassigned" ? " · 未归属" : ""}</span>
                  <span className="block text-muted-foreground">{root.files.length} 个文件{root.dirs?.length ? ` · ${root.dirs.length} 个目录摘要` : ""}</span>
                </button>
              ))}
            </nav>
          )}
          {currentRoot?.reason && <p role="status" className="text-caption text-muted-foreground">{currentRoot.reason}</p>}
          {!!currentRoot?.skipped?.length && <p role="status" className="text-caption text-muted-foreground">以下路径未能读取，未参与比对：{currentRoot.skipped.slice(0, 6).join("、")}{currentRoot.skipped.length > 6 ? " 等" : ""}</p>}
          {currentRoot?.unavailable && <p role="status" className="text-caption text-destructive">这个目录的差异采集失败。</p>}
          {currentRoot && rootSubtitle(currentRoot) && <p className="break-all text-caption text-muted-foreground">{currentRoot.path && <span>{currentRoot.path} · </span>}{rootSubtitle(currentRoot)}</p>}
          {currentRoot?.attribution === "shared" && <p role="status" className="text-caption text-muted-foreground">这个目录是用户自己的目录，采集到的改动无法区分哪个是 agent 写的。</p>}
          <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-3 md:flex-row">
            <nav aria-label="修改文件" className="max-h-36 shrink-0 overflow-auto md:max-h-none md:w-64">
              {currentFiles.map((item, index) => (
                <button key={item.path} type="button" aria-current={index === selected ? "true" : undefined} onClick={() => setSelected(index)} className={cn("flex min-h-11 w-full items-center gap-2 rounded px-2 py-2 text-left text-caption hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring", index === selected && "bg-accent font-semibold")}>
                  <span className="shrink-0 text-muted-foreground">{changeLabel(item)}</span>
                  <span className="min-w-0 flex-1 break-all">{item.path}</span>
                  <span className="shrink-0 font-mono tabular-nums">
                    {item.redacted
                      ? <span className="text-muted-foreground">已脱敏</span>
                      : <>
                        {item.added > 0 && <span className="text-success">+{item.added} </span>}
                        {item.deleted > 0 && <span className="text-destructive">−{item.deleted}</span>}
                        {item.added === 0 && item.deleted === 0 && <span className="text-muted-foreground">{formatBytes(item.size)}</span>}
                      </>}
                  </span>
                </button>
              ))}
              {!!currentRoot?.dirs?.length && (
                <ul className="mt-2 flex flex-col gap-1">{currentRoot.dirs.map((dir) => <DirRow key={dir.path} dir={dir} />)}</ul>
              )}
            </nav>
            <section aria-label="文件差异" className="min-h-0 min-w-0 flex-1 overflow-auto rounded border bg-background">
              {file ? <>
                <h3 className="sticky left-0 top-0 break-all border-b bg-muted px-3 py-2 text-caption font-medium">{file.path}</h3>
                <PatchBody file={file} />
              </> : <p className="p-3 text-body text-muted-foreground">{textChanges.length > 0 ? "本次运行只修改了平台文本，见上方改动。" : "这个分组里没有可展示的文件改动。"}</p>}
            </section>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
