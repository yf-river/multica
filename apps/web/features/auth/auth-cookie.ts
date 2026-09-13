/**
 * 「已登录」cookie 的写入/清除工具（浏览器端专用）。
 *
 * proxy（中间件）靠 multica_logged_in 判断是否带着登录会话做根路径/旧路由重定向，
 * 本模块是登录/登出时设置或删除该 cookie 的唯一入口。
 */

/** 会话标记 cookie 名。 */
const COOKIE_NAME = "multica_logged_in";

/** 写入会话 cookie（一年有效），在登录成功后调用。 */
export function setLoggedInCookie() {
  document.cookie = `${COOKIE_NAME}=1; path=/; max-age=31536000; samesite=lax`;
}

/** 清除会话 cookie，在登出时调用。 */
export function clearLoggedInCookie() {
  document.cookie = `${COOKIE_NAME}=; path=/; max-age=0`;
}
