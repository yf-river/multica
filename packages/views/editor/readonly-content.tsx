"use client";

/**
 * ReadonlyContent——对规范 <RichContent> 的兼容包裹层。
 *
 * 渲染器本身已迁到 packages/views/rich-content/（MUL-4922），让 Chat、issue
 * 描述与评论共用**同一份** Markdown 解析、消毒、围栏代码分发、mention、链接
 * 与附件实现。本文件保留，只为让既有「文档密度」调用方（评论卡片、issue
 * 详情、autopilot 详情、Markdown 附件预览）保持 import 路径与 props。
 *
 * 不要在此重新引入渲染逻辑。新行为应放 RichContent，那里所有界面一次全收到。
 */

import { memo } from "react";
import type { Attachment } from "@multica/core/types";
import { RichContent } from "../rich-content";

interface ReadonlyContentProps {
  content: string;
  className?: string;
  /**
   * 周边实体（评论 / issue 正文）关联的附件。调用方应传稳定引用；父组件
   * 每次渲染都给新数组会击穿 memo。
   */
  attachments?: Attachment[];
}

/**
 * ReadonlyContent——只读渲染 markdown 内容（文档密度）。
 */
export const ReadonlyContent = memo(function ReadonlyContent({
  content,
  className,
  attachments,
}: ReadonlyContentProps) {
  return (
    <RichContent
      content={content}
      attachments={attachments}
      density="document"
      phase="settled"
      className={className}
    />
  );
});
