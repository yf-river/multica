export function extractPromptVariables(content: string): string[] {
  const names = new Set<string>();
  for (const match of content.matchAll(/\{\{\s*([^{}\n\r]+?)\s*\}\}/g)) {
    const name = match[1]?.trim();
    if (name) names.add(name);
  }
  return [...names];
}

export function allPromptTrialVariablesFilled(variableNames: string[], variables: Record<string, string>): boolean {
  return variableNames.every((name) => Boolean(variables[name]?.trim()));
}

export function summarizePromptTrialVariables(variables: Record<string, unknown> | null | undefined): string | null {
  const entries = Object.entries(variables ?? {})
    .map(([name, value]) => [name.trim(), String(value ?? "").trim()] as const)
    .filter(([name, value]) => name && value);

  return entries.length > 0 ? entries.map(([name, value]) => `${name}=${value}`).join("，") : null;
}
