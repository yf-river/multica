"use client";

/**
 * MentionView——编辑器里把 @mention 渲染成行内元素的 NodeView。
 *
 * 成员/agent mention：带 .mention 样式的纯 "@Name" 文本。
 * issue/project mention 渲染与只读内容相同的可导航卡片（IssueMentionCard /
 * ProjectMentionCard），这样点击行为（单击、加修饰键点击、中键）不会在编辑
 * 与只读两种界面间漂移。编辑器的 ProseMirror 点击处理器会跳过
 * `[data-node-view-wrapper]` 里的内容，所以卡片里的 AppLink 独自拥有点击。
 *
 * issue 卡片尺寸：必须能放进段落行盒（14px * 1.625 = 22.75px）。卡片是
 * text-caption（12px）+ py-0.5 + 边框 ≈ 共 22px。CSS 里给
 * `[data-node-view-wrapper]` 的 `vertical-align: middle` 负责行盒对齐；设到
 * 内层元素无效，因为包裹层才是最外层的行内元素。
 */

import { NodeViewWrapper } from "@tiptap/react";
import type { NodeViewProps } from "@tiptap/react";
import { IssueMentionCard } from "../../issues/components/issue-mention-card";
import { ProjectMentionCard } from "../../projects/components/project-mention-card";

export function MentionView({ node }: NodeViewProps) {
  const { type, id, label } = node.attrs;

  // stopPropagation 镜像只读渲染器的 mention 包裹层：卡片点击不能漏给外层
  // 点击处理器。
  if (type === "issue") {
    return (
      <NodeViewWrapper
        as="span"
        className="inline"
        onClick={(e: React.MouseEvent) => e.stopPropagation()}
      >
        <IssueMentionCard issueId={id} fallbackLabel={label} />
      </NodeViewWrapper>
    );
  }

  if (type === "project") {
    return (
      <NodeViewWrapper
        as="span"
        className="inline"
        onClick={(e: React.MouseEvent) => e.stopPropagation()}
      >
        <ProjectMentionCard projectId={id} fallbackLabel={label} />
      </NodeViewWrapper>
    );
  }

  return (
    <NodeViewWrapper as="span" className="inline">
      <span className="mention">@{label ?? id}</span>
    </NodeViewWrapper>
  );
}
