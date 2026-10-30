import { api } from "@multica/core/api";

export function createInsightAttachments() {
  const urls = new Map<string, Promise<string>>();
  const live = new Set<string>();
  const imageUrl = (
    sessionId: string,
    attachment: { attachmentId: string },
  ) => {
    const key = sessionId + ":" + attachment.attachmentId;
    let pending = urls.get(key);
    if (!pending) {
      pending = api
        .getContextInsightAttachment(sessionId, attachment.attachmentId)
        .then((blob) => {
          const url = URL.createObjectURL(blob);
          live.add(url);
          return url;
        })
        .catch((error) => {
          urls.delete(key);
          throw error;
        });
      urls.set(key, pending);
    }
    return pending;
  };
  return {
    imageUrl,
    async open(
      sessionId: string,
      attachment: { attachmentId: string; name?: string },
    ) {
      const url = await imageUrl(sessionId, attachment);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = attachment.name ?? attachment.attachmentId;
      anchor.click();
      return true;
    },
    dispose() {
      for (const url of live) URL.revokeObjectURL(url);
      live.clear();
      urls.clear();
    },
  };
}
