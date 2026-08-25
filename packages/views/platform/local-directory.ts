export function isDesktopShell(): boolean {
  return typeof window !== "undefined" && "desktopAPI" in window;
}
