import { useEffect, useState } from "react";
import type { DaemonStatus } from "../../../shared/daemon-types";

interface DaemonIdentity {
  daemonId: string | null;
  deviceName: string | null;
}

export interface LocalDaemonIdentity {
  status: DaemonStatus;
  localDaemonId: string | null;
  localMachineName: string | null;
}

export function useLocalDaemonIdentity(): LocalDaemonIdentity {
  const [status, setStatus] = useState<DaemonStatus>({ state: "stopped" });
  // Keep the last daemon identity after stop events so desktop pages can
  // continue grouping the local machine instead of reclassifying it as remote.
  const [lastIdentity, setLastIdentity] = useState<DaemonIdentity>({
    daemonId: null,
    deviceName: null,
  });
  const [hostName, setHostName] = useState<string | null>(null);

  useEffect(() => {
    function apply(nextStatus: DaemonStatus): void {
      setStatus(nextStatus);
      if (nextStatus.daemonId) {
        setLastIdentity({
          daemonId: nextStatus.daemonId,
          deviceName: nextStatus.deviceName ?? null,
        });
      }
    }

    window.daemonAPI.getStatus().then(apply);
    window.daemonAPI.getHostName().then((name) => setHostName(name || null));
    return window.daemonAPI.onStatusChange(apply);
  }, []);

  return {
    status,
    localDaemonId: status.daemonId ?? lastIdentity.daemonId,
    localMachineName: status.deviceName ?? lastIdentity.deviceName ?? hostName,
  };
}
