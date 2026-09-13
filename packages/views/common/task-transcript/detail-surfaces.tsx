// 转录里单个"步骤输入/结果载荷"的渲染面：根据载荷类型（单文件 diff、整文件写入、
// 多文件 patch、纯文本工具输出）选择对应的高亮与展示方式，并为超长内容提供
// "淡出 + 显示全部"的折叠。从转录弹窗中拆出来，让弹窗文件只关心运行本身，这里只
// 关心如何渲染一段载荷。

"use client";

import { useMemo, useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { redactSecrets } from "./redact";
import type { TraceDiffLine, TracePatchFile } from "./trace-event-presenter";
import { highlightBlock, highlightToLines, languageForPath } from "./diff-highlight";
import { useT } from "../../i18n";
import "../../editor/styles/code.css";
import "./task-transcript.css";

// 长内容淡出在"显示全部"按钮后面，而不是在虚拟化列表里塞一个嵌套滚动条。
type HighlightedSides = Record<"add" | "remove" | "context", string[] | null> | null;

/**
 * 把 diff 行渲染成带增删列的 JSX。高亮按侧查表——每一侧都作为独立整块高亮过，
 * 某一侧高亮失败则只把那侧回退为纯文本，不影响其他侧。
 * @param lines diff 行数据。
 * @param highlighted 每侧已按行分好的高亮 HTML（null 表示该侧无高亮）。
 */
export function renderDiffRows(lines: TraceDiffLine[], highlighted: HighlightedSides) {
  const cursor: Record<"add" | "remove" | "context", number> = { add: 0, remove: 0, context: 0 };

  return lines.map((line, index) => {
    if (line.kind === "gap") {
      return (
        // 转录事件一经持久化就不可变，所以 index 做 key 是稳定的。
        <div key={index} className="select-none text-faint-foreground" aria-hidden>
          {"  ⋯"}
        </div>
      );
    }

    const kind = line.kind;
    const html = highlighted?.[kind]?.[cursor[kind]];
    cursor[kind] += 1;

    return (
      <div
        key={index}
        className={cn(
          "-mx-1 px-1",
          kind === "add" && "bg-success/10",
          kind === "remove" && "bg-destructive/10",
          // 只有变更行给增删列染色；代码本身保留语法色，让 diff 读起来就像它来自
          // 的那个文件。没有高亮时则整行用增/删语义色兜底。
          !html && kind === "add" && "text-success",
          !html && kind === "remove" && "text-destructive",
          !html && kind === "context" && "text-muted-foreground",
        )}
      >
        <span
          aria-hidden
          className={cn(
            "select-none",
            kind === "add" && "text-success",
            kind === "remove" && "text-destructive",
          )}
        >
          {kind === "add" ? "+" : kind === "remove" ? "-" : " "}
        </span>{" "}
        {html === undefined ? (
          redactSecrets(line.text)
        ) : (
          // 只有 lowlight 的输出会到这里：文本已被 hast 序列化器转义，元素全是它
          // 自己的 `hljs-*` span。
          <span className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
        )}
      </div>
    );
  });
}

/**
 * 整文件写入的展示面。没有"改动前"那一侧，所以按"纯内容 + 行数"来读，而不是
 * 全量新增的 diff——那种场景下每条 `+` 增删列都不携带任何信息。
 * @param text 文件全文。
 * @param lineCount 文件总行数。
 * @param path 文件路径（决定高亮语言）。
 */
export function FileWriteSurface({
  text,
  lineCount,
  path,
}: {
  text: string;
  lineCount: number;
  path: string;
}) {
  return (
    <div>
      <div className="px-3 pt-2 font-mono text-micro text-success">+{lineCount}</div>
      <ToolDetailSurface text={redactSecrets(text)} language={languageForPath(path)} />
    </div>
  );
}

/**
 * 多文件补丁的展示面：每个文件一个分区，各自复用单文件展示组件。Codex 的
 * `patch_apply` 一次调用常常碰多个文件，把它们揉成一个整体会丢掉"哪个改动属于
 * 哪个文件"。
 * @param files 补丁涉及的文件列表。
 * @param truncated 是否整体被截断。
 */
export function PatchDetailSurface({
  files,
  truncated,
}: {
  files: TracePatchFile[];
  truncated: boolean;
}) {
  const { t } = useT("agents");
  return (
    <div className="divide-y divide-border/40">
      {files.map((file, index) => (
        // 转录事件一经持久化就不可变，所以 path:index 组合 key 是稳定的。
        <div key={`${file.path}:${index}`}>
          <div className="flex items-center gap-2 px-3 pt-2 font-mono text-micro">
            {file.changeKind && (
              <span
                className={cn(
                  "shrink-0 uppercase",
                  file.changeKind === "add" && "text-success",
                  file.changeKind === "delete" && "text-destructive",
                  file.changeKind === "update" && "text-muted-foreground",
                )}
              >
                {file.changeKind}
              </span>
            )}
            <span className="truncate text-muted-foreground">{file.path}</span>
            {file.movePath && (
              <>
                <span className="shrink-0 text-faint-foreground" aria-hidden>
                  →
                </span>
                <span className="truncate text-muted-foreground">{file.movePath}</span>
              </>
            )}
          </div>
          {file.body.kind === "diff" ? (
            <DiffDetailSurface lines={file.body.lines} path={file.path} />
          ) : file.body.kind === "file" ? (
            <FileWriteSurface
              text={file.body.text}
              lineCount={file.body.lineCount}
              path={file.path}
            />
          ) : (
            <div className="px-3 pb-2 pt-1 font-mono text-micro text-muted-foreground">
              {file.truncated
                ? t(($) => $.transcript.patch_body_truncated)
                : t(($) => $.transcript.patch_no_content)}
            </div>
          )}
        </div>
      ))}
      {truncated && (
        <div className="px-3 py-2 font-mono text-micro text-muted-foreground">
          {t(($) => $.transcript.patch_truncated)}
        </div>
      )}
    </div>
  );
}

/**
 * 单文件变更展示面。按 diff（而非两个被转义的字符串字面量）来读；外壳与文本面
 * 共用同一套淡出/"显示全部"，让两类内容在虚拟化列表里的行为一致。
 * @param lines diff 行数据。
 * @param path 文件路径（决定高亮语言）。
 */
export function DiffDetailSurface({ lines, path }: { lines: TraceDiffLine[]; path: string }) {
  const { t } = useT("agents");
  const [showAll, setShowAll] = useState(false);
  const isLong = lines.length > 14;
  const added = lines.filter((l) => l.kind === "add").length;
  const removed = lines.filter((l) => l.kind === "remove").length;

  // 每一侧按整块高亮，多行字符串/注释才能保持语法一致，再按行拆开放进 diff 增删
  // 列。useMemo 只在组件真正挂载（即 diff 被展开）时才跑，不浪费。
  const highlighted = useMemo(() => {
    const language = languageForPath(path);
    if (!language) return null;
    const sides: Record<"add" | "remove" | "context", string[] | null> = {
      add: null,
      remove: null,
      context: null,
    };
    for (const kind of ["add", "remove", "context"] as const) {
      const side = lines.filter((l) => l.kind === kind);
      if (side.length > 0) {
        sides[kind] = highlightToLines(side.map((l) => l.text).join("\n"), language);
      }
    }
    return sides;
  }, [lines, path]);

  return (
    <div className="relative">
      <div className="flex items-center gap-2 px-3 pt-2 font-mono text-micro text-muted-foreground">
        {added > 0 && <span className="text-success">+{added}</span>}
        {removed > 0 && <span className="text-destructive">-{removed}</span>}
      </div>
      <pre
        className={cn(
          "transcript-code px-3 pb-3 pt-1 font-mono text-micro whitespace-pre-wrap break-all",
          isLong && !showAll && "max-h-52 overflow-hidden",
        )}
      >
        {renderDiffRows(lines, highlighted)}
      </pre>
      {isLong && !showAll && (
        <div className="absolute inset-x-0 bottom-0 flex h-12 items-end justify-center rounded-b-md bg-gradient-to-b from-transparent to-background">
          <button
            type="button"
            onClick={() => setShowAll(true)}
            // 按钮底色必须不透明：单靠渐变盖不住被裁剪的行，透明的标签会叠在残行上
            // 两边都看不清。
            className="mb-1.5 rounded border bg-background px-2 py-0.5 text-micro text-muted-foreground shadow-sm transition-colors hover:bg-accent hover:text-foreground"
          >
            {t(($) => $.transcript.show_all)}
          </button>
        </div>
      )}
    </div>
  );
}

/**
 * 通用工具载荷展示面：一段文本（命令输出、工具结果等）的代码块渲染，支持语法
 * 高亮与长内容折叠。脱敏由调用方决定是否执行（纯文本 diff 行/整文件写入在各自
 * 入口脱敏），本组件只负责展示。
 * @param text 展示文本。
 * @param language 语法名，只有文件内容带语言；命令输出和 JSON 保持纯文本。
 */
export function ToolDetailSurface({ text, language }: { text: string; language?: string }) {
  const { t } = useT("agents");
  const [showAll, setShowAll] = useState(false);
  const isLong = text.length > 1600 || text.split("\n").length > 14;
  // 只有文件内容带语言；命令输出和 JSON 保持纯文本。
  const html = useMemo(() => (language ? highlightBlock(text, language) : null), [text, language]);

  return (
    <div className="relative">
      <pre
        className={cn(
          "transcript-code p-3 font-mono text-micro text-muted-foreground whitespace-pre-wrap break-all",
          isLong && !showAll && "max-h-52 overflow-hidden",
        )}
      >
        {html === null ? (
          text
        ) : (
          // 只有 lowlight 的输出会到这里：hast 序列化器已转义文本，元素全是它
          // 自己的 `hljs-*` span。
          <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
        )}
      </pre>
      {isLong && !showAll && (
        <div className="absolute inset-x-0 bottom-0 flex h-12 items-end justify-center rounded-b-md bg-gradient-to-b from-transparent to-background">
          <button
            type="button"
            onClick={() => setShowAll(true)}
            // 按钮底色必须不透明：单靠渐变盖不住被裁剪的行，透明的标签会叠在残行上
            // 两边都看不清。
            className="mb-1.5 rounded border bg-background px-2 py-0.5 text-micro text-muted-foreground shadow-sm transition-colors hover:bg-accent hover:text-foreground"
          >
            {t(($) => $.transcript.show_all)}
          </button>
        </div>
      )}
    </div>
  );
}
