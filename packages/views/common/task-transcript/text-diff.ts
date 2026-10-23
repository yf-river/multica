export type DiffLine = { kind: " " | "-" | "+"; text: string };

const MAX_CELLS = 2_000_000;

function splitLines(value: string): string[] {
  if (!value) return [];
  return value.replace(/\n$/, "").split("\n");
}

/**
 * Line diff for platform text (a skill body, an agent prompt). Prompt edits are
 * compared the same way a file patch is, so a reviewer does not have to read two
 * full documents to see what changed.
 */
export function diffText(before: string, after: string): DiffLine[] {
  const a = splitLines(before);
  const b = splitLines(after);
  let prefix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix]) prefix++;
  let suffix = 0;
  while (suffix < a.length - prefix && suffix < b.length - prefix && a[a.length - 1 - suffix] === b[b.length - 1 - suffix]) suffix++;
  const head = a.slice(0, prefix).map((text) => ({ kind: " " as const, text }));
  const middleA = a.slice(prefix, a.length - suffix);
  const middleB = b.slice(prefix, b.length - suffix);
  const tail = a.slice(a.length - suffix).map((text) => ({ kind: " " as const, text }));
  if (middleA.length === 0 || middleB.length === 0 || middleA.length * middleB.length > MAX_CELLS) {
    return [...head, ...middleA.map((text) => ({ kind: "-" as const, text })), ...middleB.map((text) => ({ kind: "+" as const, text })), ...tail];
  }
  const width = middleB.length + 1;
  const table = new Int32Array((middleA.length + 1) * width);
  const cell = (i: number, j: number) => table[i * width + j] ?? 0;
  for (let i = middleA.length - 1; i >= 0; i--) {
    for (let j = middleB.length - 1; j >= 0; j--) {
      table[i * width + j] = middleA[i] === middleB[j]
        ? cell(i + 1, j + 1) + 1
        : Math.max(cell(i + 1, j), cell(i, j + 1));
    }
  }
  const middle: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < middleA.length && j < middleB.length) {
    if (middleA[i] === middleB[j]) {
      middle.push({ kind: " ", text: middleA[i] ?? "" });
      i++;
      j++;
    } else if (cell(i + 1, j) >= cell(i, j + 1)) {
      middle.push({ kind: "-", text: middleA[i] ?? "" });
      i++;
    } else {
      middle.push({ kind: "+", text: middleB[j] ?? "" });
      j++;
    }
  }
  while (i < middleA.length) middle.push({ kind: "-", text: middleA[i++] ?? "" });
  while (j < middleB.length) middle.push({ kind: "+", text: middleB[j++] ?? "" });
  return [...head, ...middle, ...tail];
}
