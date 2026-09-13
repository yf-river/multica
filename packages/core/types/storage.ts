// 本文件定义抽象的键值存储适配器接口（localStorage 风格的薄封装）。

/** 键值存储适配器：与 localStorage API 形状一致，便于在不同环境注入实现。 */
export interface StorageAdapter {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}
