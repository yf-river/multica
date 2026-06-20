import type { PromptLibraryVariable } from "../types";

export interface RenderPromptTemplateInput {
  content: string;
  variables?: PromptLibraryVariable[];
  values?: Record<string, string>;
}

export interface RenderPromptTemplateResult {
  rendered: string;
  usedVariables: string[];
  missingVariables: string[];
}

const VARIABLE_PATTERN = /\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}/g;

export function renderPromptTemplate(input: RenderPromptTemplateInput): RenderPromptTemplateResult {
  const values = input.values ?? {};
  const defaults = new Map(
    (input.variables ?? [])
      .filter((variable) => variable.default_value !== undefined)
      .map((variable) => [variable.name, variable.default_value ?? ""]),
  );
  const used = new Set<string>();
  const missing = new Set<string>();

  const rendered = input.content.replace(VARIABLE_PATTERN, (match, rawName: string) => {
    const name = rawName.trim();
    used.add(name);
    const value = values[name] ?? defaults.get(name);
    if (value === undefined) {
      missing.add(name);
      return match;
    }
    return value;
  });

  return {
    rendered,
    usedVariables: [...used],
    missingVariables: [...missing],
  };
}
