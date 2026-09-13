// 本文件是 diagnostics 模块的对外出口（barrel）。

export { installFreezeWatchdog } from "./freeze-watchdog";
export {
  bucketDiagnosticPath,
  getDiagnosticRoute,
  resetDiagnosticContext,
  setDiagnosticRoute,
} from "./diagnostic-context";
