import { PromptLibraryPage } from "./prompt-library-page";
import type { TrainingWorkbenchViewId } from "@multica/core/training";

export function TrainingWorkbenchPage({ activeView }: { activeView: TrainingWorkbenchViewId }) {
  return <PromptLibraryPage activeView={activeView} showPromptEditor={false} />;
}
