import fs from "node:fs";
import { expect } from "@playwright/test";
import { loadGoalTestBrowserAudit, loginGoalTest, launchGoalTestBrowser } from "./lib/goal-test-browser-audit.mjs";
import { repoRoot } from './lib/goal-test-audit-env.mjs';
import path from 'node:path';

const cfg=await loadGoalTestBrowserAudit();
const deploymentPath=path.join(repoRoot,'.run/deployments/goal-test-prod.json');
const releaseId=JSON.parse(fs.readFileSync(deploymentPath,"utf8")).release_id;
const token=await loginGoalTest(cfg);
const {browser,context}=await launchGoalTestBrowser(cfg.browserURL,token);
const results=[];
const errors=[];
const page=await context.newPage();
page.on("pageerror",e=>errors.push(e.message.slice(0,300)));
const response=await context.request.get(cfg.browserURL+"/api/workspaces");
const workspaces=await response.json();
const slug=(Array.isArray(workspaces)?workspaces:workspaces.items)[0].slug;
const href=route=>`/${slug}/${route}`;
async function ready(route,label){
  await page.waitForURL(url=>url.pathname+url.search===href(route));
  await expect(page.locator('main').getByRole("heading",{name:label,exact:true}).first()).toBeVisible();
  await expect(page.locator('main [data-slot="skeleton"]:visible')).toHaveCount(0);
}
async function navigate(route,label){
  if(route === "cooking"){
    await navigate("meals", "饮食");
    await page.getByRole("link", {name:"浏览菜谱", exact:true}).click();
    await ready(route,label);
    return;
  }
  await page.locator(`[data-sidebar="sidebar"] a[href=${JSON.stringify(href(route))}]`).first().click();
  await ready(route,label);
}
try{
  await page.goto(cfg.browserURL+href("issues"));await ready("issues","任务");
  await navigate("inbox","收件箱");await navigate("projects","项目");
  await page.goBack();await ready("inbox","收件箱");
  await page.goForward();await ready("projects","项目");
  results.push({scenario:"前进后退",ok:true});
  let routeRequests=0;
  const count=r=>{if(new URL(r.url()).pathname===href("inbox")&&r.resourceType()==="fetch")routeRequests++;};
  page.on("request",count);
  await navigate("inbox","收件箱");
  page.off("request",count);
  expect(routeRequests).toBe(0);
  results.push({scenario:"已访问菜单复用路由",ok:true,route_requests:routeRequests});
  for(const route of ["projects","agents","inbox","issues","projects","issues"]){
    await page.locator(`[data-sidebar="sidebar"] a[href=${JSON.stringify(href(route))}]`).first().click();
  }
  await ready("issues","任务");results.push({scenario:"连续快速切换",ok:true});

  // 收藏只操作一个原本未收藏的条目，最后通过真实 API 恢复。
  await navigate("cooking","烹饪指南");
  const favorite=page.locator('main button[aria-pressed="false"][aria-label^="收藏"]').first();
  await expect(favorite).toBeVisible();
  const name=(await favorite.getAttribute("aria-label")).slice(2);
  let entryId;
  let changed=false;
  try{
    const write=page.waitForResponse(r=>r.request().method()==="PUT"&&r.url().includes("/favorite"));
    await favorite.click();
    const written=await write;
    expect(written.ok()).toBe(true);entryId=new URL(written.url()).pathname.split("/").at(-1);changed=true;
    await expect(page.getByRole("button",{name:`取消收藏${name}`,exact:true})).toHaveAttribute("aria-pressed","true");
    await navigate("inbox","收件箱");
    await navigate("cooking","烹饪指南");
    await expect(page.getByRole("button",{name:`取消收藏${name}`,exact:true})).toHaveAttribute("aria-pressed","true");
    results.push({scenario:"业务数据更新后返回缓存页面",ok:true});
  }finally{
    if(changed){
      const rollback=await context.request.delete(`${cfg.backendURL}/api/cooking/favorites/${entryId}`,{headers:{Authorization:`Bearer ${token}`,"X-Workspace-Slug":slug}});
      expect(rollback.ok()).toBe(true);
      results.push({scenario:"测试收藏恢复原状态",ok:true,status:rollback.status()});
    }
  }

  await navigate("inbox","收件箱");await navigate("projects","项目");
  await page.waitForTimeout(31000);
  routeRequests=0;page.on("request",count);
  await navigate("inbox","收件箱");page.off("request",count);
  expect(routeRequests).toBeGreaterThan(0);
  results.push({scenario:"30 秒到期后重新取路由",ok:true,route_requests:routeRequests});
  const forbidden=await context.request.get(cfg.browserURL+"/api/workspaces/00000000-0000-0000-0000-000000000001",{headers:{"X-Workspace-Slug":slug}});
  expect([403,404]).toContain(forbidden.status());
  results.push({scenario:"无权限资源仍由 API 拒绝",ok:true,status:forbidden.status()});
  await page.locator('[data-sidebar="header"] [data-slot="dropdown-menu-trigger"]').first().click();
  await page.getByRole("menuitem",{name:"退出登录",exact:true}).click();
  await page.waitForURL(url=>url.pathname==="/login");
  await page.goBack();await page.waitForURL(url=>url.pathname==="/login");
  await expect(page.locator('[data-sidebar="sidebar"]')).toHaveCount(0);
  results.push({scenario:"退出登录后后退不能显示工作区",ok:true});
  expect(errors).toEqual([]);
  expect(JSON.parse(fs.readFileSync(deploymentPath,"utf8")).release_id).toBe(releaseId);
}finally{
  fs.writeFileSync(".run/evidence/menu-performance/regression.json",JSON.stringify({release_id:releaseId,results,errors},null,2));
  await browser.close();
}
console.log(JSON.stringify({results,errors},null,2));
