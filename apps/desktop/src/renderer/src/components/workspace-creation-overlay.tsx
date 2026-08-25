import { useQuery } from "@tanstack/react-query";
import { NewWorkspacePage } from "@multica/views/workspace/new-workspace-page";
import { useNavigation } from "@multica/views/navigation";
import { paths } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceCreationOverlayStore } from "@/stores/workspace-creation-overlay-store";

/** Renders the desktop workspace-creation flow above the tab system. */
export function WorkspaceCreationOverlay() {
  const isOpen = useWorkspaceCreationOverlayStore((state) => state.isOpen);
  return isOpen ? <WorkspaceCreationOverlayContent /> : null;
}

function WorkspaceCreationOverlayContent() {
  const close = useWorkspaceCreationOverlayStore((state) => state.close);
  const { push } = useNavigation();
  const { data: workspaces = [] } = useQuery(workspaceListOptions());

  // A zero-workspace user must finish the flow or log out. Existing users may
  // close the overlay and return to their current workspace.
  const onBack = workspaces.length > 0 ? close : undefined;

  return (
    <div className="fixed inset-0 z-50 flex flex-col overflow-auto bg-background">
      <NewWorkspacePage
        onSuccess={(workspace) =>
          push(paths.workspace(workspace.slug).issues())
        }
        onBack={onBack}
      />
    </div>
  );
}
