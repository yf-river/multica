export function promptLibraryTestTranslation(selector: (value: unknown) => unknown): string {
  const pathProxy = (path: string[]): unknown =>
    new Proxy(() => undefined, {
      get: (_target, property) => {
        if (property === Symbol.toPrimitive) return () => path.join(".");
        return pathProxy([...path, String(property)]);
      },
    });
  return String(selector(pathProxy([])));
}
