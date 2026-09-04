export function daemonRuntimesDocsHref(): string {
  return "https://multica.ai/docs/zh/daemon-runtimes";
}

export function customRuntimeDocsHref(): string {
  return `${daemonRuntimesDocsHref()}#${encodeURIComponent("自定义运行时配置")}`;
}
