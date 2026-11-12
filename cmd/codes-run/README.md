 # CodeS 非交互入口

这个入口运行一次主 Agent Loop，供外部管理程序调度。独立于 `cmd/main.go`，不启动 TUI。

在项目根目录编译（Windows）：

```powershell
go build -o CodeS-run.exe ./cmd/codes-run
```

调用示例：

```powershell
.\CodeS-run.exe `
  --workspace "D:\swebench\case-001" `
  --config "D:\CodeS\config.yaml" `
  --provider "my-provider" `
  --model "model-id" `
  --prompt-file "D:\swebench\tasks\case-001.txt" `
  --run-id "case-001" `
  --max-turns 100 `
  --timeout 30m `
  --heartbeat-interval 10s `
  --execution-mode full_access
```

这是独立可执行文件，无须添加 `run` 子命令。WSL/Linux 使用 `go build -o codes-run ./cmd/codes-run`。

## 参数

| 参数 | 默认值 / 含义 |
| --- | --- |
| `--workspace` | `.`，任务工作区；应为每个并行任务准备独立目录 |
| `--config` | `config.yaml`，配置路径 |
| `--provider` | 必填，配置中的 provider 名称 |
| `--model` | 必填，直接指定模型 ID，不请求模型列表 |
| `--prompt` | 直接传任务文本，与 `--prompt-file` 二选一 |
| `--prompt-file` | 读取 UTF-8 文件；`-` 表示读取标准输入直到 EOF |
| `--run-id` | 可选，省略时生成随机值，所有事件都携带该值 |
| `--max-turns` | `100`，必须大于零 |
| `--timeout` | `30m`，从开始运行计时，包含输入读取、初始化、Agent 和收尾；`0` 禁用 |
| `--heartbeat-interval` | `10s`；`0` 禁用 |
| `--execution-mode` | 省略时沿用配置，支持 `manual` / `full_access` |

所有相对参数路径均相对于启动目录，`--workspace` 不改变配置和任务文件的解析基准。
API 密钥仍从配置指定的环境变量读取，不通过命令行传入。
`manual` 模式没有审批界面，需要审批的写入和命令会被拒绝，并反馈给模型；
自动修复请显式使用 `--execution-mode full_access`。
该入口使用内置工具，不主动连接配置中的 MCP 服务。

每次调用新建会话，记录沿用 `<workspace>/.codes/sessions/`。
`session_created` 和最终事件携带绝对路径 `session_file`。
初始化失败可能尚未创建会话；按照现有 App 行为，收尾时空会话会被删除。

## 输出协议（version 1）

stdout 仅输出 UTF-8 JSONL，每行一个完整事件，无颜色或终端控制序列。
stderr 输出诊断信息。`--help` 将帮助写入 stderr 并以 0 退出，不输出事件。

每个事件均有 `version`、`event`、`run_id`、`pid`、递增 `seq`、UTC `time`、
`elapsed_ms`、`phase` 和 `step`（尚未进入轮次时为 0）。
管理程序应按行解析，并允许新增字段和未知事件。

| event | 含义 / 附加字段 |
| --- | --- |
| `started` | 命令行参数有效，开始运行；`provider`、`model` |
| `status` | 输入读取或初始化阶段；初始化时包含 `workspace` |
| `session_created` | 会话准备完成；`session_file`、`execution_mode` |
| `run_started` | 主 Agent 开始运行 |
| `step_started` | 开始一轮模型请求 |
| `text_delta` | 模型返回的文本增量 `delta` |
| `tools_proposed` | 模型提出工具调用，`count` 为数量 |
| `tool_started` | 开始执行工具；`tool`、`call_id` |
| `tool_finished` | 工具返回；`tool`、`call_id`、`status`；Bash 有命令结果时另含 `command_status`、`command_exit_code`、`command_timed_out` |
| `observation_recorded` | 工具结果已持久化；`tool`、`call_id`、`status`；Bash 有命令结果时另含上述命令字段 |
| `step_completed` | 当前轮次结束 |
| `heartbeat` | `idle_ms`、`phase_elapsed_ms`、`active_tools` |
| `cancel_requested` | 超时或收到中断；`reason`，此后仍可能有收尾事件 |
| `loop_completed` | 主 Loop 正常结束，尚未完成进程收尾 |
| `closing` | 正在取消/等待后台任务、保存记录 |
| `completed` | Loop 正常结束且收尾成功；`exit_code: 0`、`steps`、`final_content`、`usage` |
| `stopped` | 未正常完成；`exit_code`、`reason` |
| `failed` | 初始化、运行或收尾失败；`exit_code`、`error` |

`usage` 包含 `input_tokens` 和 `output_tokens`，沿用主 Loop 的现有统计，
不包含独立子 Agent 的用量。当前接口不提供失败/中止时的累计用量。
完整工具内容保存在聊天记录中，不重复写入事件流。
`status` 表示工具调用状态；`command_status` 独立表示 Bash 命令执行状态，取值为
`success`、`failed` 或 `timed_out`。命令退出码非零时，工具调用仍可为 `success`。
超时优先于退出码判定。未产生命令结果时不输出命令字段。
被拒绝或未知的工具可能只有 `observation_recorded`，没有 started/finished 对。

心跳的 `idle_ms` 是距上次非心跳事件的时间；心跳本身不会将其归零。
`active_tools` 是正在执行的主 Loop 工具列表，每项包含 `call_id` 和 `tool`。
心跳证明监控代码仍在运行，不能证明模型服务或工具有进展；长时间无文本也可能是正常推理。
工具调用参数增量、子 Agent 内部进展目前不会单独输出。

## 完成、退出和管理程序

管理程序必须持续读取 stdout；stderr 可直接重定向到每个任务自己的日志文件。
若两者都使用管道，必须同时消费，防止缓冲区写满而阻塞。
该入口以同步写入提供背压；管理程序停止读取时，事件和心跳输出也可能阻塞。

主 Loop 的 `RunCompleted` 表示调用完成，不判断代码修复是否正确。
App 完成收尾后才输出唯一终态事件（`completed` / `stopped` / `failed`）。
正常情况下终态事件的 `exit_code` 与实际进程退出码一致；
输出管道损坏、强制终止或崩溃时可能没有终态事件，应以进程退出状态为准。

| 退出码 | 含义 |
| --- | --- |
| `0` | 正常完成（或显示帮助） |
| `1` | 初始化、Agent、收尾或事件输出失败 |
| `2` | 命令行参数错误 |、
| `3` | 轮数上限、模型长度限制、内容过滤等受控停止 |
| `124` | 总超时 |
| `130` | 收到中断或终止信号后取消 |

总超时通过 context 发起取消，随后等待 Agent 和 App 收尾，所以并非到点强制退出。
心跳在初始化和收尾阶段仍会继续。外部管理程序应保留独立的超时与收尾宽限时间，
必要时终止整个进程树；尤其在 Windows 下不能假定结束父进程会自动结束 shell 的后代。
Windows 的强制终止不会走信号收尾路径，不保证最后事件或记录落盘。

此入口不额外设置单次模型请求超时，不改变现有工具超时和沙箱行为。
它也不导出补丁或执行 SWE-bench 评分，这些由外部管理程序负责。
