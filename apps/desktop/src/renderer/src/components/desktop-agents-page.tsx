import { AgentsPage } from "@multica/views/agents";
import { useLocalDaemonIdentity } from "./use-local-daemon-identity";

/**
 * Desktop wrapper around the shared `AgentsPage`. Bridges the Electron
 * `daemonAPI` (main-process daemon state) into the page so the runtime
 * machine filter can render the Local section the same way the Runtimes
 * page does — without these props the page falls back to grouping
 * every local-mode runtime under "Remote" with a generic title, which
 * breaks the "drill from a machine into its agents" promise of the
 * filter.
 *
 * Mirrors `DesktopRuntimesPage`: we cache the last seen daemon
 * identity so the Local row doesn't get reclassified as Remote when
 * the daemon is stopped (which would null out `status.daemonId`), and
 * we fall back to the OS hostname so the section label stays useful
 * even when the app doesn't manage the running daemon (WSL2 etc.).
 */
export function DesktopAgentsPage() {
  const { localDaemonId, localMachineName } = useLocalDaemonIdentity();

  return (
    <AgentsPage
      localDaemonId={localDaemonId}
      localMachineName={localMachineName}
      // Desktop owns a local machine for the lifetime of the app, even
      // while the daemon is stopped or hasn't registered yet. The shared
      // page synthesizes a placeholder local row so the filter dropdown
      // still has a Local option to pick in the empty window.
      hasLocalMachine
    />
  );
}
