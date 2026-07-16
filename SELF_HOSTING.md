# Self-Hosting Guide

Deploy Multica on your own infrastructure in minutes.

## Architecture

| Component | Description | Technology |
|-----------|-------------|------------|
| **Backend** | REST API + WebSocket server | Go (single binary) |
| **Frontend** | Web application | Next.js 16 |
| **Database** | Primary data store | PostgreSQL 17 with pgvector |

Each user who runs AI agents locally also installs the **`multica` CLI** and runs the **agent daemon** on their own machine.

## Quick Install (Recommended)

Two commands to set up everything — server, CLI, and configuration:

```bash
# 1. Install CLI + provision the self-host server
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash -s -- --with-server

# 2. Configure CLI, authenticate, and start the daemon
multica setup self-host
```

This installs the `multica` CLI, checks out the latest self-host assets, pulls the official Multica images from GHCR, and configures everything for localhost.

Open http://localhost:3000 and log in with an account name and password. See [Step 2 — Log In](#step-2--log-in) for details.

> **Prerequisites:** Docker and Docker Compose must be installed. The script checks for this and provides install links if missing.
>
> **CLI only?** If the self-host server is already running and you only need the CLI on a macOS/Linux machine, install it with Homebrew:
>
> ```bash
> brew install multica-ai/tap/multica
> ```

---

## Step-by-Step Setup (Alternative)

If you prefer to run each step manually:

### Step 1 — Start the Server

**Prerequisites:** Docker and Docker Compose.

```bash
git clone https://github.com/multica-ai/multica.git
cd multica
make selfhost
```

`make selfhost` automatically creates `.env` from the example, generates a random `JWT_SECRET`, and starts all services via Docker Compose.

By default it pulls the latest stable release images from GHCR. To build the backend/web from your current checkout instead, run `make selfhost-build`.
If the selected GHCR tag has not been published yet, `make selfhost` now tells you to fall back to `make selfhost-build`.
`make selfhost-build` uses local `multica-backend:dev` / `multica-web:dev` tags, so it does not overwrite the pulled `:latest` images.

Once ready:

- **Frontend:** http://localhost:3000
- **Backend API:** http://localhost:8080

> **Note:** If you prefer to run the Docker Compose steps manually, see [Manual Docker Compose Setup](#manual-docker-compose-setup) below.

### Step 2 — Log In

Open http://localhost:3000 in your browser and log in with an account name and password. When signup is enabled, the first successful login creates the account; later logins require the same password.

Changes to `ALLOW_SIGNUP` and `DISABLE_WORKSPACE_CREATION` take effect after restarting the backend / compose stack. The web UI reads both from `/api/config` at runtime, so no web rebuild is needed. See [Advanced Configuration → Signup Controls](SELF_HOSTING_ADVANCED.md#signup-controls-optional) for the recommended sequence to lock down workspace creation.

### Step 3 — Install CLI & Start Daemon

The daemon runs on your local machine (not inside Docker). It detects installed AI agent CLIs, registers them with the server, and executes tasks when agents are assigned work.

Each team member who wants to run AI agents locally needs to:

### a) Install the CLI and an AI agent

```bash
brew install multica-ai/tap/multica
```

You also need at least one AI agent CLI installed:
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude` on PATH)
- [Codex](https://github.com/openai/codex) (`codex` on PATH)
- [GitHub Copilot CLI](https://docs.github.com/en/copilot) (`copilot` on PATH)
- [OpenClaw](https://github.com/openclaw/openclaw) (`openclaw` on PATH)
- [OpenCode](https://github.com/anomalyco/opencode) (`opencode` on PATH)
- [Hermes](https://github.com/NousResearch/hermes) (`hermes` on PATH)
- Gemini (`gemini` on PATH)
- [Pi](https://pi.dev/) (`pi` on PATH)
- [Cursor Agent](https://cursor.com/) (`cursor-agent` on PATH)
- Kimi (`kimi` on PATH)
- Kiro CLI (`kiro-cli` on PATH)

### b) One-command setup

```bash
multica setup self-host
```

This automatically:
1. Configures the CLI to connect to `localhost` (ports 8080/3000)
2. Opens your browser for authentication
3. Discovers your workspaces
4. Starts the daemon in the background

For on-premise deployments with custom domains:

```bash
multica setup self-host --server-url https://api.example.com --app-url https://app.example.com
```

To verify the daemon is running:

```bash
multica daemon status
```

> **Alternative:** If you prefer manual steps, see [Manual CLI Configuration](#manual-cli-configuration) below.

### Step 4 — Verify & Start Using

1. Open your workspace in the web app at http://localhost:3000
2. Navigate to **Settings → Runtimes** — you should see your machine listed
3. Go to **Settings → Agents** and create a new agent
4. Create an issue and assign it to your agent — it will pick up the task automatically

---

## Kubernetes Deployment (Alternative)

If you already run a Kubernetes cluster, you can deploy Multica there instead of Docker Compose using the released OCI Helm chart at `oci://ghcr.io/multica-ai/charts/multica` or the source chart at [`deploy/helm/multica/`](deploy/helm/multica/). It targets a typical k3s / k8s setup with an Ingress controller and a default `ReadWriteOnce` StorageClass — authored against k3s + Traefik + `local-path`, and should work on any cluster with minor tweaks.

The chart creates the following resources in the target namespace:

- `multica-postgres` — `pgvector/pgvector:pg17` backed by a 10Gi PVC
- `multica-backend` — Go API/WS server. Backed by a 5Gi `ReadWriteOnce` uploads PVC by default; set `backend.uploads.persistence.enabled=false` when you have configured S3 (`backend.config.s3Bucket`) and don't want the chart to declare the PVC at all.
- `multica-frontend` — Next.js standalone server
- Two `Ingress` resources: the web host routes `/api`, `/ws`, `/auth`, and
  `/uploads` directly to the release's backend Service and all other paths to
  the frontend; the backend host routes directly to the same backend Service
- `multica-config` ConfigMap (rendered from `values.yaml`)

The `multica-secrets` Secret is **not** managed by the chart — you create it once with `kubectl` so real values never need to land in git.

> **Prerequisites:** `kubectl` and Helm 3.13+ (or Helm 4) configured for the target cluster, an Ingress controller (Traefik / NGINX), and a default StorageClass.

### Step 1 — Point hostnames at the cluster

The chart defaults to `multica.dev.lan` (web) and `api.multica.dev.lan` (backend). Pick one of:

- **`/etc/hosts`** on every machine that needs access (developer laptops + the machine running the daemon):

  ```text
  192.168.1.206  multica.dev.lan api.multica.dev.lan
  ```

  Replace `192.168.1.206` with any node IP where your Ingress controller's Service is reachable.

- **Local DNS** (Pi-hole, Unbound, etc.): add A records for both hostnames pointing at the cluster Ingress IP.

To use different hostnames, override the matching values at install time (see [Step 4](#step-4--install-the-chart)) — `ingress.frontend.host`, `ingress.backend.host`, plus `backend.config.appUrl`, `backend.config.frontendOrigin`, and `backend.config.localUploadBaseUrl`.

### Step 2 — Create the namespace

```bash
kubectl create namespace multica
```

### Step 3 — Create the `multica-secrets` Secret

The chart references this Secret by name. Create it once with random values:

```bash
kubectl -n multica create secret generic multica-secrets \
  --from-literal=JWT_SECRET="$(openssl rand -hex 32)" \
  --from-literal=POSTGRES_PASSWORD="$(openssl rand -hex 16)" \
  --from-literal=CLOUDFRONT_PRIVATE_KEY=""
```

Leave optional values empty for now — you can fill them in later (see [Step 5 — Log In](#step-5--log-in)).

### Step 4 — Install the chart

```bash
helm install multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica
```

Released chart versions strip the leading `v` from the Git tag. For example, release tag `v0.3.5` publishes chart version `0.3.5`; the chart defaults the backend and frontend image tags to `v0.3.5`.

To override defaults, export the chart values, edit them, and pass them with `-f`:

```bash
helm show values oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> > my-values.yaml
# edit my-values.yaml — e.g. change ingress hosts, image tags, resource limits
helm install multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica \
  -f my-values.yaml
```

When developing from a checkout, use the local chart path instead:

```bash
helm install multica deploy/helm/multica -n multica
```

Watch the pods come up:

```bash
kubectl -n multica get pods -w
```

On a cold cluster the backend can sit `Running` but not `Ready` for a few minutes while it waits on PostgreSQL and runs migrations — a startupProbe absorbs this, so the pod should not restart. Once the backend reports `Ready`, migrations have completed and `/healthz` returns OK:

```bash
curl -H "Host: api.multica.dev.lan" http://<ingress-ip>/healthz
# {"status":"ok","checks":{"db":"ok","migrations":"ok"}}
```

Then open http://multica.dev.lan in your browser.

### Step 5 — Log In

The chart defaults to `APP_ENV=production` (set in `values.yaml` under `backend.config.appEnv`). Open the frontend and log in with an account name and password.

`ALLOW_SIGNUP`, `ALLOWED_ACCOUNTS`, and `DISABLE_WORKSPACE_CREATION` live under `backend.config.*` in `values.yaml` (as `allowSignup`, `allowedAccounts`, and `disableWorkspaceCreation`). After `helm upgrade`, the backend pod rolls automatically because the ConfigMap hash changes; the web UI reads signup/workspace creation flags from `/api/config` at runtime, so no web rebuild is needed.

### Step 6 — Install CLI & Start Daemon

The daemon runs on your local machine, not in the cluster. Install the CLI and an AI agent as in [Step 3](#step-3--install-cli--start-daemon) above, then point the CLI at your Ingress hostnames:

```bash
multica setup self-host \
  --server-url http://api.multica.dev.lan \
  --app-url http://multica.dev.lan
```

Make sure the machine running the daemon has the same `/etc/hosts` (or DNS) entries from [Step 1](#step-1--point-hostnames-at-the-cluster).

### Updating

To pull the latest images without changing the chart version when your values still use the mutable `latest` image tag:

```bash
kubectl -n multica rollout restart deploy/multica-backend deploy/multica-frontend
```

To upgrade to a specific Multica release, upgrade to the matching chart version. The released chart defaults its app images to the matching Git tag:

```bash
helm upgrade multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica \
  -f my-values.yaml
```

If you need to override the app images independently from the chart version, set the image tags in your values file:

```yaml
images:
  backend:
    tag: v0.2.4
  frontend:
    tag: v0.2.4
```

Then run the same upgrade command with `-f my-values.yaml`:

```bash
helm upgrade multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica \
  -f my-values.yaml
```

To roll back if an upgrade goes sideways:

```bash
helm -n multica rollback multica
```

### Tearing down

```bash
# Remove the workloads but keep the PVCs and the Secret
helm -n multica uninstall multica

# Wipe everything, including PostgreSQL data and uploads
kubectl delete namespace multica
```

---

## Usage Dashboard Rollup

The Usage / Runtime dashboards read from a derived `task_usage_hourly` table populated by `rollup_task_usage_hourly()`. As of MUL-2957 the backend runs this rollup **in-process** on every replica via a DB-backed scheduler (`sys_cron_executions`); a fresh self-host install needs no operator action and the bundled `pgvector/pgvector:pg17` image works without changes — you do **not** need to swap it for an image that ships `pg_cron`, register an external cron job, set up a systemd timer, or run a Kubernetes `CronJob`.

Multiple backend replicas are safe: each replica ticks every 30 seconds and tries to claim the current 5-minute UTC plan, but the unique key `(job_name, scope_kind, scope_id, plan_time)` means only one wins each plan. Inspect steady-state operation:

```sql
SELECT plan_time, status, attempt, runner_id,
       error_code, error_msg, started_at, finished_at
  FROM sys_cron_executions
 WHERE job_name = 'rollup_task_usage_hourly'
 ORDER BY plan_time DESC
 LIMIT 20;
```

Full scheduler and audit-table details live in [Advanced Configuration → Usage Dashboard Rollup](SELF_HOSTING_ADVANCED.md#usage-dashboard-rollup).

## Stopping Services

If you installed via the install script:

```bash
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash -s -- --stop
```

If you cloned the repo manually:

```bash
# Stop the Docker Compose services (backend, frontend, database)
make selfhost-stop

# Stop the local daemon
multica daemon stop
```

## Switching to Multica Cloud

If you've been self-hosting and want to switch your CLI to [Multica Cloud](https://multica.ai):

```bash
multica setup
```

This reconfigures the CLI for multica.ai, re-authenticates, and restarts the daemon. You will be prompted before overwriting the existing configuration.

> Your local Docker services are unaffected. Stop them separately if you no longer need them.

## Upgrading

```bash
docker compose -f docker-compose.selfhost.yml pull
docker compose -f docker-compose.selfhost.yml up -d
```

Pin `MULTICA_IMAGE_TAG` in `.env` to an exact version like `v0.2.4` if you want to stay on a specific release. Migrations run automatically on backend startup.
If the selected GHCR tag has not been published yet, fall back to `make selfhost-build` or `docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build`.

---

## Manual Docker Compose Setup

If you prefer running Docker Compose steps manually instead of `make selfhost`:

```bash
git clone https://github.com/multica-ai/multica.git
cd multica
cp .env.example .env
```

Edit `.env` — at minimum, change `JWT_SECRET`:

```bash
JWT_SECRET=$(openssl rand -hex 32)
```

Then start everything:

```bash
docker compose -f docker-compose.selfhost.yml pull
docker compose -f docker-compose.selfhost.yml up -d
```

## Manual CLI Configuration

If you prefer configuring the CLI step by step instead of `multica setup`:

```bash
# Point CLI to your local server
multica config set server_url http://localhost:8080
multica config set app_url http://localhost:3000

# Login (opens browser)
multica login

# Start the daemon
multica daemon start
```

For production deployments with TLS:

```bash
multica config set app_url https://app.example.com
multica config set server_url https://api.example.com
multica login
multica daemon start
```

## Advanced Configuration

For environment variables, manual setup (without Docker), reverse proxy configuration, database setup, and more, see the [Advanced Configuration Guide](SELF_HOSTING_ADVANCED.md).
