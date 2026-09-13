"use client";

/**
 * 附件预览页路由：整屏预览附件（HTML iframe 等）。
 * 刻意放在 (dashboard) 组之外——dashboard 布局有侧边栏与顶栏，而本页希望把
 * 整个视口让给 iframe。workspace 解析仍在父级 [workspaceSlug] 布局里做，
 * 因此 useWorkspaceId() 依旧可用。
 */
import { use } from "react";
import { useSearchParams } from "next/navigation";
import { AttachmentPreviewPage } from "@multica/views/attachments";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

/**
 * 附件预览路由。
 *
 * @param props.params - 动态参数，含附件 id（Promise 形式）。
 * @returns 全屏附件预览（带错误边界）。
 */
export default function AttachmentPreviewWebPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const search = useSearchParams();
  const filename = search.get("name") ?? undefined;

  return (
    <ErrorBoundary resetKeys={[id]}>
      <AttachmentPreviewPage attachmentId={id} filename={filename} />
    </ErrorBoundary>
  );
}
