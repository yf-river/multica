import type { CSSProperties } from "react";

export function createColumnTrackVars<Key extends string>(
  widths: Record<Key, number>,
  fixedWidth: number,
  variables: Record<Key, `--${string}`>,
  minWidthVariable: `--${string}`,
) {
  const keys = Object.keys(widths) as Key[];

  return (
    isVisible: (key: Key) => boolean,
    additionalWidth = 0,
  ): CSSProperties => {
    const visibleWidth = keys.reduce(
      (sum, key) => sum + (isVisible(key) ? widths[key] : 0),
      0,
    );
    return {
      ...Object.fromEntries(
        keys.map((key) => [
          variables[key],
          isVisible(key) ? `${widths[key]}px` : "0px",
        ]),
      ),
      [minWidthVariable]: `${fixedWidth + visibleWidth + additionalWidth}px`,
    } as CSSProperties;
  };
}
