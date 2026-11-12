# CodeS 运行说明

CodeS 是一个用于学习 Agent 开发的终端程序。交互入口提供模型选择、对话、工具调用和会话恢复；另有供外部程序调用的非交互入口。项目主要在 Windows 环境下使用，也可以在 WSL 2 / Linux 中运行。

## 环境要求

- **Go 1.26.0 或更新版本**（以项目 `go.mod` 为准）。首次构建需要联网下载 Go 模块。
- **可交互的终端**：Windows 推荐 PowerShell 或 Windows Terminal；WSL 2 使用 Linux 终端。
- **可访问的 OpenAI 兼容模型服务**及其 API Key。交互界面会向服务的模型列表接口获取可选模型，因此服务也需要支持该接口。
- **Git**：仅在使用基于 Git Worktree 的隔离子 Agent 时需要；目标仓库须至少有一个提交。

在终端执行 `go version`，确认 Go 版本满足要求。Windows 和 WSL 2 是两套独立环境：在哪一侧启动 CodeS，就在那一侧安装 Go、设置环境变量并使用对应的路径格式。

## 配置模型服务

程序从**启动时的当前目录**读取 `config.yaml`。仓库已有一份配置示例，可以按自己的模型服务修改。最小配置如下：

```yaml
execution_mode: manual
providers:
  - name: my-provider
    protocol: openai-compatible
    base_url: https://example.com/v1
    api_key_env: MY_MODEL_API_KEY
mcp_servers: []
```

将 `base_url` 换成服务提供的 OpenAI 兼容 API 地址，`name` 可自行命名。`api_key_env` 填写**环境变量名**，不要在 YAML 中填入密钥。当前支持的 provider `protocol` 值为 `openai-compatible`。可以在 `providers` 下配置多个服务商。

启动程序前，在同一个终端设置环境变量。例如，Windows PowerShell：

```powershell
$env:MY_MODEL_API_KEY = "你的 API Key"
```

WSL 2 / Linux：

```bash
export MY_MODEL_API_KEY='你的 API Key'
```

仓库自带的 `config.yaml` 使用 `DEEPSEEK_API_KEY` 和 `WSL_OPENROUTE_KEY` 作为两个 provider 的密钥环境变量名；如果直接沿用该文件，请设置所选 provider 对应的变量。密钥只在当前终端会话中设置即可，不要提交到仓库。

`execution_mode` 支持 `manual` 和 `full_access`。交互入口在 `manual` 模式下会对写文件和执行命令请求确认；`full_access` 会直接允许这些操作。建议先使用 `manual`。`mcp_servers` 可以设为空列表；当前两个启动入口均不会自动连接配置中的 MCP 服务。

## 启动交互界面

先进入仓库根目录（即含有 `go.mod` 和 `config.yaml` 的目录）。在 **Windows PowerShell** 中：

```powershell
go build -o CodeS.exe ./cmd
.\CodeS.exe
```

也可以直接运行 `go run ./cmd`。如果在 **WSL 2 / Linux** 中构建：

```bash
go build -o codes ./cmd
./codes
```

启动后输入 `/model`，依次选择配置的 provider 和模型；模型选择菜单使用方向键和 Enter。选好模型后输入消息并按 Enter 开始对话。输入 `/help` 可查看界面命令，`/mode` 可查看当前执行模式，`/resume` 可恢复已有会话，`/exit` 可退出。

交互入口把当前目录作为工作区，会话记录保存在该目录的 `.codes/sessions/`。如果想操作其他项目，应在目标工作区准备好适用的 `config.yaml`，并从该目录启动程序；直接在 CodeS 仓库根目录启动时，工作区就是 CodeS 仓库。

## 非交互运行（可选）

需要从脚本或管理程序执行一次任务时，使用独立入口 `cmd/codes-run`。在 Windows PowerShell 中，从仓库根目录构建并运行，例如：

```powershell
go build -o CodeS-run.exe ./cmd/codes-run
.\CodeS-run.exe --workspace "C:\path\to\workspace" --config "config.yaml" --provider "my-provider" --model "模型 ID" --prompt "请分析当前项目"
```

它从环境变量读取所选 provider 的 API Key，并将 JSONL 事件写入标准输出。`--provider`、`--model` 必填，任务内容通过 `--prompt` 或 `--prompt-file` 二选一提供；`--workspace` 默认是当前目录。该入口没有交互审批界面：在 `manual` 模式下，需要审批的写入和命令会被拒绝；确实需要自动执行这些操作时，可显式传入 `--execution-mode full_access`。完整参数和输出协议见 [非交互入口说明](cmd/codes-run/README.md)。

## 平台差异

- Windows 下普通命令工具使用 `cmd.exe`。当前没有 Windows OS 沙箱后端；隔离子 Agent 的 Bash 命令会报错，文件工具仍可使用。
- WSL 2 / Linux 下命令工具使用 Unix shell。隔离子 Agent 执行 Bash 需要系统安装 `bubblewrap`（`bwrap`）且环境允许创建所需命名空间；缺少后端时该功能会报错。详细限制见 [Worktree 隔离说明](internal/gitworktree/README.md)。
- Windows 路径（如 `C:\...`）与 WSL 路径（如 `/mnt/c/...`）不能混用；非交互入口的相对配置路径和任务文件路径均相对于启动目录。
