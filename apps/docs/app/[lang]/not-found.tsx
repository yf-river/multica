import Link from "next/link";

export default function NotFound() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-4 px-4 py-24 text-center">
      <h1 className="text-3xl font-semibold">页面不存在</h1>
      <p className="text-fd-muted-foreground">
        你访问的文档页面不存在或已经移动。
      </p>
      <Link
        href="/"
        className="inline-flex items-center rounded-md bg-fd-primary px-4 py-2 text-sm font-medium text-fd-primary-foreground transition-colors hover:bg-fd-primary/90"
      >
        返回文档首页
      </Link>
    </main>
  );
}
