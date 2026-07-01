#!/usr/bin/env node

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");

const repos = {
  usercenter: "/data/ida/user-center",
  gateway: "/data/ida/gateway",
  deployment: "/data/ida/ida-deployment",
};

const evidence = {
  schema: "ida.quick_entry_cross_service_sandbox.v2",
  generated_at: now,
  sandbox_mode: "service-process",
  endpoint: "POST /v1/user-center/list-quick-access",
  semantic_guard: "gateway derives UIN from authenticated request context and ignores supplied query/body userId",
  repos: {},
  checks: [],
  cross_service_curl: {
    ok: false,
    sandbox_gateway_url: "",
    public_gateway_url: "",
    curl_path: "/v1/user-center/list-quick-access?userId=999",
    boundary: "curl -> gateway HTTP process -> gateway quick-access HTTP-to-gRPC sandbox -> generated user-center gRPC client -> TCP user-center gRPC process -> real user-center server/logic -> DB test double",
    auth_boundary: "sandbox gateway process maps x-goal-test-uin to gRPC metadata normally populated by gateway token middleware",
  },
};

fs.mkdirSync(artifactRoot, { recursive: true });

try {
  for (const [key, repo] of Object.entries(repos)) {
    evidence.repos[key] = repoState(repo);
  }

  runCheck("usercenter_logic", repos.usercenter, [
    "go", "test", "./internal/logic",
    "-run", "Test(Create|List|Update|Delete)QuickAccess",
    "-count=1",
  ]);

  runCheck("usercenter_grpc_server", repos.usercenter, [
    "bash", "-lc",
    "rg -n 'CreateQuickAccess|UpdateQuickAccess|DeleteQuickAccess|ListQuickAccess' internal/server/usercenterserver.go proto/user_center.proto pb/usercenterpb/user_center_grpc.pb.go",
  ]);

  evidence.cross_service_curl = {
    ...evidence.cross_service_curl,
    ...(await runQuickEntryServiceProcessCurl()),
  };

  runCheck("gateway_route_and_apidata", repos.gateway, [
    "bash", "-lc",
    [
      "tmp=$(mktemp -d)",
      "cd \"$tmp\"",
      "go work init /data/ida/gateway /data/ida/user-center",
      "GOWORK=\"$tmp/go.work\" go test /data/ida/gateway/internal/handler /data/ida/gateway/internal/apidata -count=1",
      "rc=$?",
      "rm -rf \"$tmp\"",
      "exit $rc",
    ].join("; "),
  ]);

  runCheck("ida_deployment_permission_render", repos.deployment, [
    "bash", "-lc",
    [
      "node -e \"const endpoints=['/v1/user-center/create-quick-access','/v1/user-center/update-quick-access','/v1/user-center/delete-quick-access','/v1/user-center/list-quick-access']; for (const f of ['helm/public/charts/usercenter/permissions/api/user-center.api.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode1.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode2.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode3.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode4.json']) { const j=require('./'+f); const s=JSON.stringify(j); for (const endpoint of endpoints) if (!s.includes(endpoint)) throw new Error(f + ' missing ' + endpoint); }\"",
      "helm template ida-front helm/front -f helm/front/values.yaml >/tmp/ida-front-quick-access-render.yaml",
      "rg -n '/v1/user-center/(create|update|delete|list)-quick-access' /tmp/ida-front-quick-access-render.yaml",
    ].join(" && "),
  ]);

  evidence.ok = evidence.checks.every((check) => check.ok) && evidence.cross_service_curl.ok;
  evidence.cross_service_curl.usercenter_commit = evidence.repos.usercenter.commit;
  evidence.cross_service_curl.gateway_commit = evidence.repos.gateway.commit;
  evidence.cross_service_curl.deployment_commit = evidence.repos.deployment.commit;
  evidence.cross_service_curl.usercenter_branch = evidence.repos.usercenter.branch;
  evidence.cross_service_curl.gateway_branch = evidence.repos.gateway.branch;
  evidence.cross_service_curl.deployment_branch = evidence.repos.deployment.branch;
} catch (error) {
  evidence.ok = false;
  evidence.error = error instanceof Error ? error.stack || error.message : String(error);
}

const jsonPath = path.join(artifactRoot, `quick-entry-cross-service-${stamp}.json`);
const latestPath = path.join(artifactRoot, "quick-entry-cross-service-latest.json");
fs.writeFileSync(jsonPath, `${JSON.stringify(evidence, null, 2)}\n`);
fs.writeFileSync(latestPath, `${JSON.stringify(evidence, null, 2)}\n`);

console.log(JSON.stringify({
  ok: evidence.ok,
  json: jsonPath,
  latest: latestPath,
  checks: evidence.checks.map((check) => ({ id: check.id, ok: check.ok, status: check.status })),
}, null, 2));
if (!evidence.ok) process.exitCode = 1;

async function runQuickEntryServiceProcessCurl() {
  const usercenterPort = await getFreePort();
  const gatewayPort = await getFreePort();
  const usercenterAddr = `127.0.0.1:${usercenterPort}`;
  const gatewayAddr = `127.0.0.1:${gatewayPort}`;
  const usercenterSandboxDir = path.join(repos.usercenter, ".goal-test-sandbox", "quickentry-usercenter");
  const gatewaySandboxDir = path.join(repos.gateway, ".goal-test-sandbox", "quickentry-gateway");
  const processes = [];
  const result = {
    ok: false,
    sandbox_gateway_url: `http://${gatewayAddr}/v1/user-center/list-quick-access`,
    public_gateway_url: `http://${gatewayAddr}/v1/user-center/list-quick-access`,
    usercenter_addr: usercenterAddr,
    gateway_addr: gatewayAddr,
    user_id_header: "x-goal-test-uin: test-uin",
    curl_command: [
      "curl",
      "-sS",
      "-D",
      "-",
      "-H",
      "x-goal-test-uin: test-uin",
      "-X",
      "POST",
      "-H",
      "content-type: application/json",
      "--data",
      "{\"userId\":999}",
      `http://${gatewayAddr}/v1/user-center/list-quick-access?userId=999`,
    ],
    usercenter_process: null,
    gateway_process: null,
    curl_stdout: "",
    curl_stderr: "",
    assertions: [],
  };

  try {
    fs.rmSync(path.join(repos.usercenter, ".goal-test-sandbox"), { recursive: true, force: true });
    fs.rmSync(path.join(repos.gateway, ".goal-test-sandbox"), { recursive: true, force: true });
    fs.mkdirSync(usercenterSandboxDir, { recursive: true });
    fs.mkdirSync(gatewaySandboxDir, { recursive: true });
    fs.writeFileSync(path.join(usercenterSandboxDir, "main.go"), quickEntryUserCenterMainSource());
    fs.writeFileSync(path.join(gatewaySandboxDir, "main.go"), quickEntryGatewayMainSource());
    const goWorkDir = fs.mkdtempSync(path.join(process.env.TMPDIR || "/tmp", "quick-access-go-work-"));
    execFileSync("go", ["work", "init", repos.gateway, repos.usercenter], { cwd: goWorkDir });
    result.go_work_file = path.join(goWorkDir, "go.work");

    const usercenterProcess = startProcess("quickentry-usercenter", repos.usercenter, [
      "go", "run", "./.goal-test-sandbox/quickentry-usercenter", "-listen", usercenterAddr,
    ]);
    processes.push(usercenterProcess);
    result.usercenter_process = usercenterProcess.info;
    await waitForTcp(usercenterAddr, 120000, usercenterProcess);

    const gatewayProcess = startProcess("quickentry-gateway", repos.gateway, [
      "go", "run", "./.goal-test-sandbox/quickentry-gateway", "-listen", gatewayAddr, "-usercenter", usercenterAddr,
    ], { GOFLAGS: "", GOWORK: result.go_work_file });
    processes.push(gatewayProcess);
    result.gateway_process = gatewayProcess.info;
    await waitForTcp(gatewayAddr, 120000, gatewayProcess);

    const curlOutput = execFileSync(result.curl_command[0], result.curl_command.slice(1), {
      cwd: repoRoot,
      encoding: "utf8",
      maxBuffer: 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
    });
    result.curl_stdout = curlOutput;

    const responseBody = curlOutput.slice(curlOutput.indexOf("\r\n\r\n") + 4).trim();
    result.response_body = responseBody;
    result.assertions.push(assertContains(responseBody, `"userId":101`, "gateway used authenticated context UIN to resolve userId"));
    result.assertions.push(assertNotContains(responseBody, `"userId":999`, "gateway ignored query/body userId"));
    result.assertions.push(assertContains(responseBody, `"name":"工作台"`, "response came from user-center service process quick-access list"));
    result.assertions.push(assertContains(responseBody, `"url":"/workspace"`, "quick-access URL returned"));
    result.ok = result.assertions.every((item) => item.ok);
    if (!result.ok) {
      throw new Error(`service-process curl assertions failed: ${JSON.stringify(result.assertions)}`);
    }
  } catch (error) {
    result.error = error instanceof Error ? error.stack || error.message : String(error);
    result.usercenter_logs = collectLogs(processes.find((item) => item.name === "quickentry-usercenter"));
    result.gateway_logs = collectLogs(processes.find((item) => item.name === "quickentry-gateway"));
  } finally {
    for (const proc of [...processes].reverse()) {
      await stopProcess(proc);
    }
    result.usercenter_logs = collectLogs(processes.find((item) => item.name === "quickentry-usercenter"));
    result.gateway_logs = collectLogs(processes.find((item) => item.name === "quickentry-gateway"));
    fs.rmSync(path.join(repos.usercenter, ".goal-test-sandbox"), { recursive: true, force: true });
    fs.rmSync(path.join(repos.gateway, ".goal-test-sandbox"), { recursive: true, force: true });
    if (result.go_work_file) fs.rmSync(path.dirname(result.go_work_file), { recursive: true, force: true });
  }

  return result;
}

function quickEntryUserCenterMainSource() {
  return `package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"time"

	"chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/dao"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/models"
	usercenterserver "chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/server"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/internal/svc"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/pb/usercenterpb"
	commonmeta "chainweaver.org.cn/chainweaver/ida/common/v5/metadata"
	"google.golang.org/grpc"
	grpcmetadata "google.golang.org/grpc/metadata"
)

type quickEntryDB struct {
	dao.DBInterface
	user *models.Users
	items []*models.UserQuickAccess
}

func (d *quickEntryDB) GetUserByUserNameOrUin(context.Context, string, string) (*models.Users, error) {
	return d.user, nil
}

func (d *quickEntryDB) GetUserSingleRole(context.Context, uint) (*models.Roles, error) {
	return &models.Roles{ID: 1, RoleType: 2, Name: "普通业务员"}, nil
}

func (d *quickEntryDB) ListActiveQuickAccessByUserID(context.Context, uint) ([]*models.UserQuickAccess, error) {
	return d.items, nil
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "listen address")
	flag.Parse()

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}

	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if md, ok := grpcmetadata.FromIncomingContext(ctx); ok {
			if vals := md.Get(commonmeta.KeyUIN); len(vals) > 0 {
				ctx = commonmeta.WithUIN(ctx, vals[0])
			}
		}
		return handler(ctx, req)
	}))
	now := time.Now()
	usercenterpb.RegisterUserCenterServer(server, usercenterserver.NewUserCenterServer(&svc.ServiceContext{
		DBHandle: &quickEntryDB{
			user: &models.Users{
				ID:       101,
				UIN:      "test-uin",
				TenantID: "tenant-from-user-center-service-process",
				Status:   models.StatusNormal,
			},
			items: []*models.UserQuickAccess{
				{ID: 1, UserID: 101, Name: "工作台", URL: "/workspace", CreatedAt: now, UpdatedAt: now},
			},
		},
	}))

	fmt.Printf("READY usercenter %s\\n", listener.Addr().String())
	if err := server.Serve(listener); err != nil {
		panic(err)
	}
}
`;
}

function quickEntryGatewayMainSource() {
  return `package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"

	commonmeta "chainweaver.org.cn/chainweaver/ida/common/v5/metadata"
	"chainweaver.org.cn/chainweaver/ida/user-center/v5/pb/usercenterpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpcmetadata "google.golang.org/grpc/metadata"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "listen address")
	usercenterAddr := flag.String("usercenter", "", "user-center grpc address")
	flag.Parse()
	if *usercenterAddr == "" {
		panic("missing -usercenter")
	}

	conn, err := grpc.NewClient(*usercenterAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	client := usercenterpb.NewUserCenterClient(conn)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/user-center/list-quick-access", func(w http.ResponseWriter, r *http.Request) {
		uin := r.Header.Get("x-goal-test-uin")
		if uin == "" {
			uin = "test-uin"
		}
		ctx := grpcmetadata.AppendToOutgoingContext(context.Background(), commonmeta.KeyUIN, uin)
		resp, err := client.ListQuickAccess(ctx, &usercenterpb.ListQuickAccessReq{})
		w.Header().Set("content-type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}
	fmt.Printf("READY gateway %s usercenter=%s\\n", listener.Addr().String(), *usercenterAddr)
	if err := http.Serve(listener, mux); err != nil {
		panic(err)
	}
}
`;
}

function startProcess(name, cwd, command, extraEnv = {}) {
  const info = {
    name,
    cwd,
    command: command.map(shellQuote).join(" "),
    pid: null,
    stdout: "",
    stderr: "",
  };
  const child = spawn(command[0], command.slice(1), {
    cwd,
    env: { ...process.env, GOFLAGS: "-mod=readonly", GOWORK: "off", ...extraEnv },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  info.pid = child.pid;
  child.stdout.on("data", (chunk) => {
    info.stdout += chunk.toString();
  });
  child.stderr.on("data", (chunk) => {
    info.stderr += chunk.toString();
  });
  return { name, child, info };
}

async function stopProcess(proc) {
  if (!proc?.child || hasExited(proc.child)) return;
  const pgid = proc.info.pid;
  try {
    process.kill(-pgid, "SIGTERM");
  } catch {}
  await waitForExit(proc.child, 1500);
  if (hasExited(proc.child)) return;
  try {
    process.kill(-pgid, "SIGKILL");
  } catch {}
  await waitForExit(proc.child, 1500);
}

function collectLogs(proc) {
  if (!proc) return null;
  return {
    stdout_tail: tail(proc.info.stdout, 200),
    stderr_tail: tail(proc.info.stderr, 200),
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
    socket.once("connect", () => {
      socket.end();
      resolve();
    });
    socket.once("error", reject);
    socket.setTimeout(1000, () => {
      socket.destroy(new Error("tcp connect timeout"));
    });
  });
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function waitForExit(child, timeoutMs) {
  if (hasExited(child)) return Promise.resolve();
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, timeoutMs);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

function hasExited(child) {
  return child.exitCode !== null || child.signalCode !== null;
}

function assertContains(text, needle, description) {
  return { ok: text.includes(needle), description, expected: needle };
}

function assertNotContains(text, needle, description) {
  return { ok: !text.includes(needle), description, forbidden: needle };
}

function runCheck(id, cwd, command) {
  const started = Date.now();
  const check = {
    id,
    cwd,
    command: command.map(shellQuote).join(" "),
    ok: false,
    status: "failed",
    duration_ms: 0,
    stdout: "",
    stderr: "",
  };
  evidence.checks.push(check);
  try {
    const output = execFileSync(command[0], command.slice(1), {
      cwd,
      encoding: "utf8",
      maxBuffer: 20 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
    });
    check.stdout = output;
    check.ok = true;
    check.status = "passed";
  } catch (error) {
    check.stdout = String(error.stdout || "");
    check.stderr = String(error.stderr || "");
    check.error = error instanceof Error ? error.message : String(error);
  } finally {
    check.duration_ms = Date.now() - started;
  }
  if (!check.ok) {
    throw new Error(`${id} failed: ${check.error || check.stderr || check.stdout}`);
  }
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
  return execFileSync(command[0], command.slice(1), {
    cwd,
    encoding: "utf8",
    maxBuffer: 1024 * 1024,
  });
}

function shellQuote(value) {
  const s = String(value);
  if (/^[A-Za-z0-9_./:=@+-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, "'\\''")}'`;
}
