import { captureEvent } from "./index";

// Pairs with the backend feedback_submitted event without sending message text.
export function captureFeedbackOpened(workspaceId?: string): void {
  captureEvent("feedback_opened", {
    source: "help_menu",
    ...(workspaceId ? { workspace_id: workspaceId } : {}),
  });
}
