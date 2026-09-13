"use client";

// 本文件是创建 Agent 弹窗里的“Instructions（行为指令）”可折叠编辑器：
// 默认收起成一行卡片（展示指令预览或占位文案），点一下展开成完整的富文本编辑器。

import { useRef, useState } from "react";
import { ChevronDown, FileText, X } from "lucide-react";
import { ContentEditor, type ContentEditorRef } from "../../editor";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

interface InstructionsEditorProps {
  /** 指令的 Markdown 文本。展开时当默认值，收起时当预览文字。 */
  value: string;
  /** 每次键入都会触发（ContentEditor 内部已做防抖）。 */
  onChange: (value: string) => void;
  /** 可选的自定义占位文案。缺省用 i18n 的“点此编写”；复制 Agent 时创建弹窗
   *  会传入针对克隆场景的特定文案。 */
  placeholder?: string;
}

/**
 * 创建 Agent 弹窗里的可折叠指令编辑器。平时保持紧凑，等用户真想写时再展开——
 * 大多数 Agent 只有在认真编写时才需要指令，而不是每次快速创建都要填。
 *
 * 两种状态：
 *   收起态 → 一张可点击的小卡片，展示 `value` 的预览（为空则显示占位文案），点一下就展开。
 *   展开态 → 完整 ContentEditor（Markdown、气泡菜单、@提及、附件上传）。标题栏右侧的
 *            “收起”按钮可折回去，已输入的内容会保留。
 */
export function InstructionsEditor({
  value,
  onChange,
  placeholder,
}: InstructionsEditorProps) {
  const { t } = useT("agents");
  const [expanded, setExpanded] = useState(false);
  const editorRef = useRef<ContentEditorRef>(null);

  const label = t(($) => $.create_dialog.instructions.label);
  const resolvedPlaceholder =
    placeholder ?? t(($) => $.create_dialog.instructions.placeholder_blank);

  const expand = () => {
    setExpanded(true);
    // 下一帧再聚焦：先让编辑器完成挂载，否则 ref 还是空的。
    setTimeout(() => editorRef.current?.focus(), 0);
  };

  if (!expanded) {
    return (
      <div>
        <div className="text-caption font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </div>
        <button
          type="button"
          onClick={expand}
          className="mt-1.5 flex w-full items-start gap-2.5 rounded-lg border bg-card px-3 py-3 text-left transition-colors hover:border-primary/40 hover:bg-accent/40"
        >
          <FileText className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            {value.trim() ? (
              // 预览：取 Markdown 的前 2 行，超长省略。
              <div className="line-clamp-2 whitespace-pre-wrap text-body text-foreground">
                {value}
              </div>
            ) : (
              <div className="text-body text-muted-foreground">{resolvedPlaceholder}</div>
            )}
          </div>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-faint-foreground" />
        </button>
      </div>
    );
  }

  return (
    <div>
      <div className="flex items-center justify-between">
        <div className="text-caption font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => setExpanded(false)}
          className="h-6 gap-1 px-2 text-caption"
        >
          <X className="h-3 w-3" />
          {t(($) => $.create_dialog.instructions.collapse)}
        </Button>
      </div>
      <div
        className={cn(
          "mt-1.5 rounded-lg border bg-card",
          "focus-within:border-primary/40",
        )}
      >
        <ContentEditor
          ref={editorRef}
          defaultValue={value}
          onUpdate={onChange}
          placeholder={t(($) => $.create_dialog.instructions.editor_placeholder)}
          className="min-h-[160px] max-h-[320px] overflow-y-auto px-3 py-2.5 text-body"
          showBubbleMenu={true}
          disableMentions={true}
        />
      </div>
    </div>
  );
}
