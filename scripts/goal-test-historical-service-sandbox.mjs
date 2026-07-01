#!/usr/bin/env node

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

const repos = {
  usercenter: "/data/ida/user-center",
  gateway: "/data/ida/gateway",
  deployment: "/data/ida/ida-deployment",
};

fs.mkdirSync(artifactRoot, { recursive: true });

const report = {
  schema: "multica.goal_test.historical_service_sandbox.v1",
  generated_at: generatedAt,
  sandbox_mode: "service-process",
  repos: Object.fromEntries(Object.entries(repos).map(([key, repo]) => [key, repoState(repo)])),
  boundary: "curl -> gateway HTTP service-process -> gateway GenericRpcMiddleware -> generated user-center gRPC client -> user-center gRPC service-process -> real user-center server/logic -> benchmark DB test double; ida-deployment apiData/render checked from real generated config",
  cases: [],
};

try {
  Object.assign(report, await runHistoricalServiceSandbox());
  runProductDeploymentCase(report);
  report.ok = report.cases.every((item) => item.ok);
} catch (error) {
  report.ok = false;
  report.error = error instanceof Error ? error.stack || error.message : String(error);
}

const jsonPath = path.join(artifactRoot, `historical-service-sandbox-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `historical-service-sandbox-${stamp}.md`);
const latestJsonPath = path.join(artifactRoot, "historical-service-sandbox-latest.json");
const latestMarkdownPath = path.join(artifactRoot, "historical-service-sandbox-latest.md");

fs.writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(markdownPath, renderMarkdown(report));
fs.writeFileSync(latestJsonPath, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(latestMarkdownPath, renderMarkdown(report));

console.log(JSON.stringify({
  ok: report.ok,
  json: jsonPath,
  markdown: markdownPath,
  latest_json: latestJsonPath,
  latest_markdown: latestMarkdownPath,
  cases: report.cases.map((item) => ({ id: item.id, ok: item.ok, status: item.status })),
}, null, 2));

if (!report.ok) process.exitCode = 1;

async function runHistoricalServiceSandbox() {
  const usercenterPort = await getFreePort();
  const gatewayPort = await getFreePort();
  const usercenterAddr = `127.0.0.1:${usercenterPort}`;
  const gatewayAddr = `127.0.0.1:${gatewayPort}`;
  const sandboxName = `historical-${process.pid}-${stamp}`;
  const usercenterSandboxRoot = path.join(repos.usercenter, ".goal-test-sandbox", sandboxName);
  const gatewaySandboxRoot = path.join(repos.gateway, ".goal-test-sandbox", sandboxName);
  const usercenterSandboxDir = path.join(usercenterSandboxRoot, "usercenter");
  const gatewaySandboxDir = path.join(gatewaySandboxRoot, "gateway");
  const processes = [];
  const result = {
    usercenter_addr: usercenterAddr,
    gateway_addr: gatewayAddr,
    gateway_base_url: `http://${gatewayAddr}`,
    usercenter_process: null,
    gateway_process: null,
  };

  try {
    fs.mkdirSync(usercenterSandboxDir, { recursive: true });
    fs.mkdirSync(gatewaySandboxDir, { recursive: true });
    fs.writeFileSync(path.join(usercenterSandboxDir, "main.go"), historicalUserCenterMainSource());
    fs.writeFileSync(path.join(gatewaySandboxDir, "main.go"), historicalGatewayMainSource());
    const gatewayModFile = path.join(gatewaySandboxDir, "gateway-go.mod");
    fs.copyFileSync(path.join(repos.gateway, "go.mod"), gatewayModFile);

    const usercenterProcess = startProcess("historical-usercenter", repos.usercenter, [
      "go", "run", `./.goal-test-sandbox/${sandboxName}/usercenter`, "-listen", usercenterAddr,
    ]);
    processes.push(usercenterProcess);
    result.usercenter_process = usercenterProcess.info;
    await waitForTcp(usercenterAddr, 120000, usercenterProcess);

    const gatewayProcess = startProcess("historical-gateway", repos.gateway, [
      "go", "run", `./.goal-test-sandbox/${sandboxName}/gateway`,
      "-listen", gatewayAddr,
      "-usercenter", usercenterAddr,
      "-apidata", path.join(repos.deployment, "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode1.json"),
    ], { GOFLAGS: `-mod=mod -modfile=${gatewayModFile}` });
    processes.push(gatewayProcess);
    result.gateway_process = gatewayProcess.info;
    await waitForTcp(gatewayAddr, 120000, gatewayProcess);

    report.cases.push(runLinkUserSpaceOrgCase(gatewayAddr));
    report.cases.push(runAnnouncementNoticeCase(gatewayAddr));
    report.cases.push(runNoticeSameSecondCursorCase(gatewayAddr));
  } finally {
    for (const proc of [...processes].reverse()) {
      await stopProcess(proc);
    }
    result.usercenter_logs = collectLogs(processes.find((item) => item.name === "historical-usercenter"));
    result.gateway_logs = collectLogs(processes.find((item) => item.name === "historical-gateway"));
    fs.rmSync(usercenterSandboxRoot, { recursive: true, force: true });
    fs.rmSync(gatewaySandboxRoot, { recursive: true, force: true });
  }

  enrichHistoricalCaseEvidence(report.cases, result);
  return result;
}

function enrichHistoricalCaseEvidence(cases, sandboxResult) {
  const usercenterOutput = sandboxResult.usercenter_logs?.stdout_tail || "";
  const gatewayOutput = sandboxResult.gateway_logs?.stdout_tail || "";
  const byID = Object.fromEntries(cases.map((item) => [item.id, item]));

  appendAssertions(byID["link-user-space-org"], [
    assertContains(usercenterOutput, "EVENT link_delete", "LinkUserSpaceOrg deleted old user-space-org relations"),
    assertContains(usercenterOutput, "EVENT link_create", "LinkUserSpaceOrg created new user-space-org relations"),
    assertContains(usercenterOutput, '"org_id":"org-a"', "LinkUserSpaceOrg retained org-a"),
    assertContains(usercenterOutput, '"org_id":"org-b"', "LinkUserSpaceOrg retained org-b"),
    assertEquals(countOccurrences(usercenterOutput, '"org_id":"org-a"'), 1, "LinkUserSpaceOrg collapsed duplicate org-a input"),
    assertContains(gatewayOutput, "/usercenter.UserCenter/LinkUserSpaceOrg", "Gateway invoked LinkUserSpaceOrg over gRPC"),
  ]);

  appendAssertions(byID["announcement-notice"], [
    assertContains(usercenterOutput, "EVENT announcement_create", "SaveAnnouncement created announcement through user-center logic"),
    assertContains(usercenterOutput, '"sender_uin":"operator-uin"', "SaveAnnouncement used request UIN from gRPC metadata"),
    assertContains(usercenterOutput, "EVENT announcement_list", "GetAnnouncementList hit user-center list logic"),
    assertContains(usercenterOutput, '"count":1', "GetAnnouncementList returned tenant-scoped announcement count"),
    assertContains(usercenterOutput, "EVENT announcement_detail", "GetAnnouncementDetail hit user-center detail logic"),
    assertContains(gatewayOutput, "/usercenter.UserCenter/SaveAnnouncement", "Gateway invoked SaveAnnouncement over gRPC"),
    assertContains(gatewayOutput, "/usercenter.UserCenter/GetAnnouncementList", "Gateway invoked GetAnnouncementList over gRPC"),
    assertContains(gatewayOutput, "/usercenter.UserCenter/GetAnnouncementDetail", "Gateway invoked GetAnnouncementDetail over gRPC"),
  ]);

  appendAssertions(byID["notice-same-second-cursor"], [
    assertContains(usercenterOutput, "EVENT sync_notice_list_after", "SyncNotice queried notices from cursor boundary"),
    assertContains(usercenterOutput, '"count":4', "SyncNotice included same-second cursor candidates"),
    assertContains(usercenterOutput, "EVENT notice_rel_create_on_conflict", "SyncNotice used idempotent relation creation"),
    assertContains(usercenterOutput, '"conflict_columns":["notice_id","tenant_id"]', "SyncNotice preserved notice_id/tenant_id idempotency key"),
    assertContains(usercenterOutput, '"notice_id":100', "SyncNotice retained same-second tenant-a notice"),
    assertContains(usercenterOutput, '"notice_id":102', "SyncNotice retained after-cursor tenant-a notice"),
    assertNotContains(usercenterOutput, '"notice_id":101', "SyncNotice skipped other-tenant notice"),
    assertContains(gatewayOutput, "/usercenter.UserCenter/SyncNotice", "Gateway invoked SyncNotice over gRPC"),
  ]);
}

function appendAssertions(item, assertions) {
  if (!item) return;
  item.assertions = [...(item.assertions || []), ...assertions];
  item.ok = item.assertions.every((assertion) => assertion.ok);
  item.status = item.ok ? item.status : "failed";
}

function runLinkUserSpaceOrgCase(gatewayAddr) {
  const curl = curlJSON(`http://${gatewayAddr}/v1/user-center/link-user-space-org`, {
    uin: "target-uin",
    spaceOrgInfo: [
      { sid: "space-a", orgId: "org-a" },
      { sid: "space-a", orgId: "org-a" },
      { sid: "space-a", orgId: "org-b" },
    ],
  });
  const assertions = [
    assertContains(curl.stdout, '"code":200', "LinkUserSpaceOrg returned success"),
  ];
  return {
    id: "link-user-space-org",
    suite: "historical-replay",
    source: "/data/ida/sopAgent/benchmarks/user-center/add-api/link-user-space-org",
    ok: assertions.every((item) => item.ok),
    status: assertions.every((item) => item.ok) ? "service_sandbox_passed" : "failed",
    curl,
    assertions,
  };
}

function runAnnouncementNoticeCase(gatewayAddr) {
  const save = curlJSON(`http://${gatewayAddr}/v1/user-center/save-announcement`, {
    title: "平台公告",
    content: "公告内容",
  });
  const list = curlJSON(`http://${gatewayAddr}/v1/user-center/get-announcement-list`, {
    pageReq: { pageNumber: 1, pageSize: 10 },
  });
  const detail = curlJSON(`http://${gatewayAddr}/v1/user-center/get-announcement-detail`, { id: 4 });
  const assertions = [
    assertContains(save.stdout, '"code":200', "SaveAnnouncement returned success"),
    assertContains(list.stdout, '"title":"平台公告"', "GetAnnouncementList returned saved title"),
    assertContains(detail.stdout, '"content":"公告内容"', "GetAnnouncementDetail returned saved content"),
  ];
  return {
    id: "announcement-notice",
    suite: "historical-replay",
    source: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/35-real-demand-pilot-and-deployment-permission-benchmark-20260616.md",
    ok: assertions.every((item) => item.ok),
    status: assertions.every((item) => item.ok) ? "service_sandbox_passed" : "failed",
    curl_sequence: [save, list, detail],
    assertions,
  };
}

function runNoticeSameSecondCursorCase(gatewayAddr) {
  const sync = curlJSON(`http://${gatewayAddr}/v1/user-center/sync-notice`, {});
  const assertions = [
    assertContains(sync.stdout, '"code":200', "SyncNotice returned success"),
  ];
  return {
    id: "notice-same-second-cursor",
    suite: "historical-replay",
    source: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/35-real-demand-pilot-and-deployment-permission-benchmark-20260616.md",
    ok: assertions.every((item) => item.ok),
    status: assertions.every((item) => item.ok) ? "service_sandbox_passed" : "failed",
    curl: sync,
    assertions,
  };
}

function runProductDeploymentCase(targetReport) {
  const checks = [];
  checks.push(runCheck("source_product_api", repos.deployment, [
    "node", "-e",
    "const j=require('./helm/public/charts/usercenter/permissions/api/product.api.json'); const s=JSON.stringify(j); if (!s.includes('/v1/product/example-operation')) throw new Error('missing product example-operation source');",
  ]));
  checks.push(runCheck("generated_gateway_apidata_modes", repos.deployment, [
    "node", "-e",
    "for (const m of [1,2,3,4]) { const s=JSON.stringify(require('./helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode'+m+'.json')); const has=s.includes('/v1/product/example-operation'); if ((m===1||m===2) && !has) throw new Error('mode '+m+' missing product route'); if ((m===3||m===4) && has) throw new Error('mode '+m+' unexpectedly contains product route'); }",
  ]));
  checks.push(runCheck("helm_front_template", repos.deployment, [
    "bash", "-lc", "helm template ida-front helm/front -f helm/front/values.yaml >/tmp/ida-front-historical-product-render.yaml",
  ]));
  const ok = checks.every((item) => item.ok);
  targetReport.cases.push({
    id: "product-api-permission",
    suite: "historical-replay",
    source: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/35-real-demand-pilot-and-deployment-permission-benchmark-20260616.md",
    ok,
    status: ok ? "deployment_gateway_config_passed" : "failed",
    boundary: "deployment permission/apiData/render only; product-service backend is outside the current three-project scope and is not faked",
    checks,
  });
}

function curlJSON(url, body) {
  const command = [
    "curl", "-sS", "-D", "-",
    "-H", "Content-Type: application/json",
    "-H", "x-goal-test-tenant-id: tenant-a",
    "-H", "x-goal-test-uin: operator-uin",
    "-X", "POST",
    "--data", JSON.stringify(body),
    url,
  ];
  try {
    const stdout = execFileSync(command[0], command.slice(1), {
      cwd: repoRoot,
      encoding: "utf8",
      maxBuffer: 1024 * 1024,
    });
    return { command: command.map(shellQuote).join(" "), ok: true, stdout, stderr: "" };
  } catch (error) {
    return {
      command: command.map(shellQuote).join(" "),
      ok: false,
      stdout: String(error.stdout || ""),
      stderr: String(error.stderr || ""),
      error: error instanceof Error ? error.message : String(error),
    };
  }
}

function historicalGatewayMainSource() {
  return `package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"sync"

	commonconfig "chainweaver.org.cn/chainweaver/ida/common/v5/config"
	commongrpc "chainweaver.org.cn/chainweaver/ida/common/v5/grpc"
	commonmeta "chainweaver.org.cn/chainweaver/ida/common/v5/metadata"
	"chainweaver.org.cn/chainweaver/ida/gateway/v5/internal/apidata"
	"chainweaver.org.cn/chainweaver/ida/gateway/v5/internal/consts"
	gatewaygrpc "chainweaver.org.cn/chainweaver/ida/gateway/v5/internal/grpc"
	"chainweaver.org.cn/chainweaver/ida/gateway/v5/internal/middleware"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "listen address")
	usercenterAddr := flag.String("usercenter", "", "user-center grpc address")
	apiDataPath := flag.String("apidata", "", "gateway apiData path")
	flag.Parse()
	if *usercenterAddr == "" || *apiDataPath == "" {
		panic("missing -usercenter or -apidata")
	}

	items, err := apidata.Load(*apiDataPath)
	if err != nil {
		panic(err)
	}
	urlMap, _ := apidata.BuildMaps(items)

	client, err := commongrpc.CreateGRPCClient(commonconfig.GrpcConf{
		Endpoint: *usercenterAddr,
		MaxRecvMsgSizeBytes: 20 * 1024 * 1024,
		MaxSendMsgSizeBytes: 20 * 1024 * 1024,
	})
	if err != nil {
		panic(err)
	}
	resolver, err := gatewaygrpc.NewProtoResolver(consts.ClientNameUserCenter)
	if err != nil {
		panic(err)
	}
	clientMap := &sync.Map{}
	clientMap.Store(consts.ClientTypeUserCenter, &gatewaygrpc.GenericClient{Conn: *client, Resolver: resolver})
	generic := middleware.NewGenericRpcMiddleware(clientMap, urlMap).Handle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = commonmeta.WithTenantID(ctx, headerOrDefault(r, "x-goal-test-tenant-id", "tenant-a"))
		ctx = commonmeta.WithUIN(ctx, headerOrDefault(r, "x-goal-test-uin", "operator-uin"))
		ctx = context.WithValue(ctx, consts.XRequestID, "goal-test-historical-service-sandbox")
		generic(w, r.WithContext(ctx))
	})

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}
	fmt.Printf("READY historical gateway %s usercenter=%s apidata=%s\\n", listener.Addr().String(), *usercenterAddr, *apiDataPath)
	if err := http.Serve(listener, mux); err != nil {
		panic(err)
	}
}

func headerOrDefault(r *http.Request, key, fallback string) string {
	if value := r.Header.Get(key); value != "" {
		return value
	}
	return fallback
}
`;
}

function historicalUserCenterMainSource() {
  return `package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"sync"
	"time"

	serverinterceptor "chainweaver.org.cn/chainweaver/ida/common/v5/grpc/interceptor/server"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/dao"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/models"
	usercenterserver "chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/server"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/svc"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/pb/usercenterpb"
	"google.golang.org/grpc"
)

type benchmarkDB struct {
	dao.DBInterface
	mu sync.Mutex
	operator *models.Users
	target *models.Users
	role *models.Roles
	spaceRels []*models.UserSpaceOrgRel
	notices []*models.Notice
	noticeRels []*models.NoticeTenantRel
	latestRelCreatedAt time.Time
}

func newBenchmarkDB() *benchmarkDB {
	base := time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC)
	return &benchmarkDB{
		operator: &models.Users{ID: 1, UIN: "operator-uin", Username: "operator", TenantID: "tenant-a", Status: models.StatusNormal},
		target: &models.Users{ID: 2, UIN: "target-uin", Username: "target", TenantID: "tenant-a", Status: models.StatusNormal},
		role: &models.Roles{ID: 10, Name: "tenant-admin", RoleType: models.RoleTypeTenantAdmin},
		latestRelCreatedAt: base,
		notices: []*models.Notice{
			{ID: 100, CreatedAt: base, UpdatedAt: base, Type: models.NoticeTypeNotice, Title: "same-second", Content: "content", Scope: "tenant:tenant-a"},
			{ID: 101, CreatedAt: base, UpdatedAt: base, Type: models.NoticeTypeNotice, Title: "other-tenant", Content: "content", Scope: "tenant:tenant-b"},
			{ID: 102, CreatedAt: base.Add(time.Second), UpdatedAt: base, Type: models.NoticeTypeNotice, Title: "after-cursor", Content: "content", Scope: "tenant:tenant-a,tenant-c"},
		},
	}
}

func (d *benchmarkDB) GetUserByUserNameOrUin(context.Context, string, string) (*models.Users, error) {
	return d.operator, nil
}

func (d *benchmarkDB) GetUserSingleRole(context.Context, uint) (*models.Roles, error) {
	return d.role, nil
}

func (d *benchmarkDB) FirstByWhere(_ context.Context, _ map[string]interface{}, out interface{}) error {
	user, ok := out.(*models.Users)
	if !ok {
		return sql.ErrNoRows
	}
	*user = *d.target
	return nil
}

func (d *benchmarkDB) GetUserRoleType(context.Context, uint) (int8, error) {
	return models.RoleTypeUser, nil
}

func (d *benchmarkDB) Transaction(fn func(dao.DBInterface) error, _ ...*sql.TxOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fn(d)
}

func (d *benchmarkDB) DeleteByWhere(_ context.Context, conditions map[string]interface{}, _ interface{}) error {
	d.spaceRels = nil
	logEvent("link_delete", conditions)
	return nil
}

func (d *benchmarkDB) CreateInBatches(_ context.Context, value interface{}, _ int) (int64, error) {
	switch v := value.(type) {
	case []*models.UserSpaceOrgRel:
		d.spaceRels = append(d.spaceRels, v...)
		logEvent("link_create", v)
		return int64(len(v)), nil
	case []*models.NoticeTenantRel:
		d.noticeRels = append(d.noticeRels, v...)
		logEvent("notice_rel_create", v)
		return int64(len(v)), nil
	default:
		return 0, sql.ErrNoRows
	}
}

func (d *benchmarkDB) CreateInBatchesWithOnConflict(_ context.Context, value interface{}, _ int, conflictColumns []string) error {
	rels, ok := value.([]*models.NoticeTenantRel)
	if !ok {
		return sql.ErrNoRows
	}
	d.noticeRels = append(d.noticeRels, rels...)
	logEvent("notice_rel_create_on_conflict", map[string]interface{}{"rels": rels, "conflict_columns": conflictColumns})
	return nil
}

func (d *benchmarkDB) ListAllTenantIDs(context.Context) ([]string, error) {
	return []string{"tenant-a", "tenant-b"}, nil
}

func (d *benchmarkDB) CreateNoticeWithScope(_ context.Context, noticeType int8, title, content, scope, senderUIN, senderRelTenantID string) error {
	notice := &models.Notice{ID: uint(len(d.notices) + 1), CreatedAt: time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC), Type: noticeType, Title: title, Content: content, Scope: scope, SenderUIN: senderUIN}
	d.notices = append(d.notices, notice)
	if senderRelTenantID != "" {
		d.noticeRels = append(d.noticeRels, &models.NoticeTenantRel{NoticeID: notice.ID, TenantID: senderRelTenantID, CreatedAt: notice.CreatedAt})
	}
	logEvent("announcement_create", notice)
	return nil
}

func (d *benchmarkDB) ListNoticesByTenant(_ context.Context, noticeType int8, tenantID string, pageNumber, pageSize int32) ([]*models.Notice, int64, error) {
	var list []*models.Notice
	for _, notice := range d.notices {
		if notice.Type == noticeType && scopeContains(notice.Scope, tenantID) {
			list = append(list, notice)
		}
	}
	logEvent("announcement_list", map[string]interface{}{"tenant_id": tenantID, "count": len(list), "page_number": pageNumber, "page_size": pageSize})
	return list, int64(len(list)), nil
}

func (d *benchmarkDB) GetNoticeByTenantAndType(_ context.Context, id uint, noticeType int8, tenantID string) (*models.Notice, error) {
	for _, notice := range d.notices {
		if notice.ID == id && notice.Type == noticeType && scopeContains(notice.Scope, tenantID) {
			logEvent("announcement_detail", map[string]interface{}{"id": id, "tenant_id": tenantID})
			return notice, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (d *benchmarkDB) GetLatestNoticeRelCreatedAtByTenant(context.Context, string) (*time.Time, error) {
	return &d.latestRelCreatedAt, nil
}

func (d *benchmarkDB) ListNoticesCreatedAfter(_ context.Context, since *time.Time, until time.Time) ([]*models.Notice, error) {
	var list []*models.Notice
	for _, notice := range d.notices {
		if since != nil && notice.CreatedAt.Before(*since) {
			continue
		}
		if notice.CreatedAt.After(until) {
			continue
		}
		list = append(list, notice)
	}
	logEvent("sync_notice_list_after", map[string]interface{}{"count": len(list)})
	return list, nil
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "listen address")
	flag.Parse()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(serverinterceptor.IncomingMetadataInterceptor()))
	usercenterpb.RegisterUserCenterServer(server, usercenterserver.NewUserCenterServer(&svc.ServiceContext{DBHandle: newBenchmarkDB()}))
	fmt.Printf("READY historical usercenter %s\\n", listener.Addr().String())
	if err := server.Serve(listener); err != nil {
		panic(err)
	}
}

func scopeContains(scope, tenantID string) bool {
	const prefix = "tenant:"
	if len(scope) < len(prefix) || scope[:len(prefix)] != prefix {
		return false
	}
	value := scope[len(prefix):]
	start := 0
	for i := 0; i <= len(value); i++ {
		if i != len(value) && value[i] != ',' {
			continue
		}
		if value[start:i] == tenantID {
			return true
		}
		start = i + 1
	}
	return false
}

func logEvent(kind string, payload interface{}) {
	data, _ := json.Marshal(payload)
	fmt.Printf("EVENT %s %s\\n", kind, string(data))
}
`;
}

function runCheck(id, cwd, command) {
  const check = { id, cwd, command: command.map(shellQuote).join(" "), ok: false, status: "failed", stdout: "", stderr: "" };
  try {
    check.stdout = execFileSync(command[0], command.slice(1), { cwd, encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
    check.ok = true;
    check.status = "passed";
  } catch (error) {
    check.stdout = String(error.stdout || "");
    check.stderr = String(error.stderr || "");
    check.error = error instanceof Error ? error.message : String(error);
  }
  return check;
}

function startProcess(name, cwd, command, extraEnv = {}) {
  const info = { name, cwd, command: command.map(shellQuote).join(" "), pid: null, stdout: "", stderr: "" };
  const child = spawn(command[0], command.slice(1), {
    cwd,
    env: { ...process.env, GOFLAGS: "-mod=readonly", GOWORK: "off", ...extraEnv },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  info.pid = child.pid;
  child.stdout.on("data", (chunk) => { info.stdout += chunk.toString(); });
  child.stderr.on("data", (chunk) => { info.stderr += chunk.toString(); });
  return { name, child, info };
}

async function stopProcess(proc) {
  if (!proc?.child || hasExited(proc.child)) return;
  try { process.kill(-proc.info.pid, "SIGTERM"); } catch {}
  await waitForExit(proc.child, 1500);
  if (hasExited(proc.child)) return;
  try { process.kill(-proc.info.pid, "SIGKILL"); } catch {}
  await waitForExit(proc.child, 1500);
}

function collectLogs(proc) {
  if (!proc) return null;
  return {
    stdout_tail: tail(proc.info.stdout, 300),
    stderr_tail: tail(proc.info.stderr, 300),
    pid: proc.info.pid,
    command: proc.info.command,
    cwd: proc.info.cwd,
  };
}

function tail(text, lines) {
  return String(text || "").split(/\r?\n/).slice(-lines).join("\n");
}

async function getFreePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => resolve(address.port));
    });
  });
}

async function waitForTcp(address, timeoutMs, proc) {
  const [host, portText] = address.split(":");
  const port = Number(portText);
  const started = Date.now();
  let lastError = null;
  while (Date.now() - started < timeoutMs) {
    if (proc.child.exitCode !== null) {
      throw new Error(`${proc.name} exited before becoming ready: stdout=${proc.info.stdout} stderr=${proc.info.stderr}`);
    }
    try {
      await tryTcp(host, port);
      return;
    } catch (error) {
      lastError = error;
      await sleep(500);
    }
  }
  throw new Error(`${proc.name} did not open ${address} within ${timeoutMs}ms: ${lastError?.message || lastError}`);
}

function tryTcp(host, port) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ host, port });
    socket.once("connect", () => { socket.end(); resolve(); });
    socket.once("error", reject);
    socket.setTimeout(1000, () => socket.destroy(new Error("tcp connect timeout")));
  });
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function waitForExit(child, timeoutMs) {
  if (hasExited(child)) return Promise.resolve();
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, timeoutMs);
    child.once("exit", () => { clearTimeout(timer); resolve(); });
  });
}

function hasExited(child) {
  return child.exitCode !== null || child.signalCode !== null;
}

function repoState(repo) {
  return {
    path: repo,
    branch: exec(repo, ["git", "branch", "--show-current"]).trim(),
    commit: exec(repo, ["git", "rev-parse", "HEAD"]).trim(),
    dirty: exec(repo, ["git", "status", "--short"]).trim() !== "",
  };
}

function exec(cwd, command) {
  return execFileSync(command[0], command.slice(1), { cwd, encoding: "utf8", maxBuffer: 1024 * 1024 });
}

function assertContains(text, needle, description) {
  return { ok: text.includes(needle), description, expected: needle };
}

function assertNotContains(text, needle, description) {
  return { ok: !text.includes(needle), description, not_expected: needle };
}

function assertEquals(actual, expected, description) {
  return { ok: actual === expected, description, actual, expected };
}

function countOccurrences(text, needle) {
  return String(text || "").split(needle).length - 1;
}

function renderMarkdown(data) {
  const lines = ["# Historical Service Sandbox", "", `Generated: ${data.generated_at}`, "", `OK: ${data.ok}`, "", "## Cases", ""];
  for (const item of data.cases) {
    lines.push(`### ${item.id}`, "", `- status: ${item.status}`, `- ok: ${item.ok}`, "");
  }
  return `${lines.join("\n")}\n`;
}

function shellQuote(value) {
  const s = String(value);
  if (/^[A-Za-z0-9_./:=@+-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, "'\\''")}'`;
}
