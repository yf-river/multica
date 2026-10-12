/** 部署身份由宿主注入；不从端口、域名或构建期变量猜测。 */
export function environmentCookieName(base: string, environmentId = ""): string {
  if (environmentId && !/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(environmentId)) {
    throw new Error("Invalid MULTICA_ENV_ID");
  }
  return environmentId ? `${base}_${environmentId}` : base;
}
