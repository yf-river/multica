# Multica CLI 安装指南（面向 AI 智能体）

> **读者：** 本文档为 AI 智能体（Claude Code、Codex 等）设计，按步骤执行。每步包含要跑的精确 shell 命令、预期结果与错误处理。

## 给用户的一条指令

把这条指令交给你的 AI 智能体：

```
Fetch https://github.com/multica-ai/multica/blob/main/CLI_INSTALL.md and follow the instructions to install Multica CLI, log in, and start the daemon on this machine.
```

---

## 步骤 1：检查 Multica CLI 是否已安装

运行：

```bash
multica version
```

- **若打印版本字符串**（如 `multica v0.x.x`）：跳到**步骤 3**。
- **若 command not found**：继续**步骤 2**。

---

## 步骤 2：安装 Multica CLI

> **Windows 用户：** 跳到下方 [选项 C：Windows（PowerShell）](#选项-cwindowspowershell)。

### 选项 A：Homebrew（首选——macOS/Linux）

检查 Homebrew 是否可用：

```bash
which brew
```

若找到 `brew`，通过 Homebrew 安装：

```bash
brew install multica-ai/tap/multica
```

然后验证：

```bash
multica version
```

若版本打印成功，跳到**步骤 3**。

后续升级用：

```bash
brew upgrade multica-ai/tap/multica
```

### 选项 B：从 GitHub Releases 下载（macOS/Linux，无 Homebrew）

若没有 Homebrew，直接下载二进制。

检测 OS 与架构，下载正确归档：

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')   # "darwin" 或 "linux"
ARCH=$(uname -m)                                # "x86_64" 或 "arm64"

# 规范化架构名
if [ "$ARCH" = "x86_64" ]; then
  ARCH="amd64"
fi

# 从 GitHub 获取最新 release tag
LATEST=$(curl -sI https://github.com/multica-ai/multica/releases/latest | grep -i '^location:' | sed 's/.*tag\///' | tr -d '\r\n')

# 下载并解压
VERSION="${LATEST#v}"
curl -sL "https://github.com/multica-ai/multica/releases/download/${LATEST}/multica-cli-${VERSION}-${OS}-${ARCH}.tar.gz" -o /tmp/multica.tar.gz
tar -xzf /tmp/multica.tar.gz -C /tmp multica
sudo mv /tmp/multica /usr/local/bin/multica
rm /tmp/multica.tar.gz
```

验证：

```bash
multica version
```

**若失败：**
- 检查 `/usr/local/bin` 是否在 `$PATH` 中。
- Linux 上可能需要 `chmod +x /usr/local/bin/multica`。
- 若无 `sudo`，装到用户可写目录：`mv /tmp/multica ~/.local/bin/multica`，并确保 `~/.local/bin` 在 `$PATH` 中。

### 选项 C：Windows（PowerShell）

在 PowerShell 中运行（无需管理员）：

```powershell
irm https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.ps1 | iex
```

这会从 GitHub Releases 下载最新 Windows 二进制，装到 `%USERPROFILE%\.multica\bin\`，并加入用户 PATH。

验证：

```powershell
multica version
```

**若失败：**
- 重启终端让更新后的 PATH 生效。
- 若用 Scoop，安装器会自动用它：`scoop bucket add multica https://github.com/multica-ai/scoop-bucket.git && scoop install multica`
- 若执行策略阻止脚本：`Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned` 后重跑。

---

## 步骤 3：登录

运行：

```bash
multica login
```

**重要：** 该命令会打开浏览器做 Multica 账号/密码认证。告诉用户：

> 「浏览器会打开 Multica 登录页，请在浏览器中完成认证后回到这里。」

等命令完成。它会自动发现并 watch 用户所属的所有工作区。

验证：

```bash
multica auth status
```

预期输出显示已认证用户与 server URL。

**若登录失败：**
- 若无浏览器（headless 环境），用户可在 `https://app.multica.ai/settings` 生成 Personal Access Token 后跑：`multica login --token <mul_...>`（用 `--token=` 加空值做交互式提示输入）。
- 若需自定义 server URL：先 `multica config set server_url <url>` 再登录。

---

## 步骤 4：启动守护进程

先检查守护进程是否在跑：

```bash
multica daemon status
```

- **若状态为 "running"**：跳到**步骤 5**。
- **若状态为 "stopped"**：启动它：

```bash
multica daemon start
```

等 3 秒后验证：

```bash
multica daemon status
```

预期输出显示 `running` 状态及检测到的智能体（如 `claude`、`codex`、`copilot`、`opencode`、`openclaw`、`hermes`、`gemini`、`pi`、`cursor-agent`）。

**若守护进程启动失败：**
- 查日志：`multica daemon logs`
- 若端口冲突，守护进程可能已在另一个 profile 下运行。
- 若未检测到智能体，确保至少一个 AI CLI（`claude`、`codex`、`copilot`、`opencode`、`openclaw`、`hermes`、`gemini`、`pi` 或 `cursor-agent`）已安装且在 `$PATH` 中。

---

## 步骤 5：验证一切正常

运行：

```bash
multica daemon status
```

确认：
1. 状态为 `running`
2. 至少列出一个智能体（如 `claude`、`codex`、`copilot`、`opencode`、`openclaw`、`hermes`、`gemini`、`pi` 或 `cursor-agent`）
3. 至少 watch 一个工作区

若智能体列表为空，告诉用户：

> 「Multica 守护进程在跑但未检测到任何 AI 智能体 CLI。请安装至少一个受支持的 CLI（`claude`、`codex`、`copilot`、`opencode`、`openclaw`、`hermes`、`gemini`、`pi` 或 `cursor-agent`），然后用 `multica daemon stop && multica daemon start` 重启守护进程。」

---

## 总结

所有步骤完成后，告诉用户：

> 「Multica CLI 已安装、守护进程在运行。工作区里的智能体现在可以在本机执行任务。用 `multica workspace list` 管理工作区，用 `multica daemon logs -f` 查看守护进程日志。」
