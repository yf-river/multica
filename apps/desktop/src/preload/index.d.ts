import type { ElectronAPI } from "@electron-toolkit/preload";
import type { DaemonAPI, DesktopAPI, UpdaterAPI } from "./index";

declare global {
  interface Window {
    electron: ElectronAPI;
    desktopAPI: DesktopAPI;
    daemonAPI: DaemonAPI;
    updater: UpdaterAPI;
  }
}

export {};
