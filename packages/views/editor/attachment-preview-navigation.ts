import { paths } from "@multica/core/paths";
import type { NavigationAdapter } from "../navigation";

export function openAttachmentPreviewPage(
  navigation: NavigationAdapter,
  workspaceSlug: string,
  attachmentId: string,
  filename: string,
) {
  const nameQuery = filename ? `?name=${encodeURIComponent(filename)}` : "";
  const path = `${paths.workspace(workspaceSlug).attachmentPreview(attachmentId)}${nameQuery}`;
  if (navigation.openInNewTab) {
    navigation.openInNewTab(path, filename, { activate: true });
    return;
  }
  window.open(
    navigation.getShareableUrl(path),
    "_blank",
    "noopener,noreferrer",
  );
}
