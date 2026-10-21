/** Resolve a relative document link to an existing file inside the skill. */
export function resolveSkillFileLink(
  currentPath: string,
  href: string,
  filePaths: readonly string[],
): string | null {
  if (!href || /^[a-z][a-z\d+.-]*:/i.test(href) || /^[/#?]/.test(href)) {
    return null;
  }

  let target: string;
  try {
    target = decodeURIComponent(href.split(/[?#]/, 1)[0]!);
  } catch {
    return null;
  }
  if (!target || target.startsWith("/") || target.includes("\\")) return null;

  const segments = currentPath.split("/").slice(0, -1);
  for (const segment of target.split("/")) {
    if (!segment || segment === ".") continue;
    if (segment === "..") {
      if (segments.length === 0) return null;
      segments.pop();
    } else {
      segments.push(segment);
    }
  }
  const path = segments.join("/");
  return filePaths.includes(path) ? path : null;
}
