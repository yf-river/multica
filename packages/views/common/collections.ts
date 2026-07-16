export function mapBy<Item, Key, Value>(
  items: readonly Item[],
  key: (item: Item) => Key,
  value: (item: Item) => Value,
) {
  const result = new Map<Key, Value>();
  for (const item of items) result.set(key(item), value(item));
  return result;
}

export function indexBy<Item, Key>(items: readonly Item[], key: (item: Item) => Key) {
  return mapBy(items, key, (item) => item);
}
