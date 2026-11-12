# SubAgent Worktree 隔离

调用 `SubAgent` 时传入 `isolation: true`。系统使用当前仓库 HEAD 创建
`codes/subagent/<随机 ID>` 分支和独立目录。父工作区的未提交修改、未跟踪文件不会复制。
默认目录为 `os.UserCacheDir()/CodeS/worktrees/codes-*/tree`，必须位于源仓库之外。

每个隔离 Agent 使用独立的 Workspace 和 ToolCatalog。explorer/reviewer 只可使用
ListFile、ReadFile、Glob、Grep；所有子 Agent 均不能调用 SubAgent 或 WorktreeCleanup。
父 PermissionManager 的限制仍然继承。未知工作区绑定的 MCP 工具不会暴露给隔离 Agent。
`isolation: false` 或省略时保留原有共享工作区行为。

Bash 的 cwd 为 Worktree，可写路径仅包含该 Worktree，Git 管理文件保持只读。
不允许整个系统临时目录写入，防止覆盖位于临时目录中的主仓库或其他子任务。
Linux 使用 bubblewrap，macOS 使用 sandbox-exec；后端缺失、包装失败或启动失败均不会
退回无沙箱执行。Windows 当前没有 OS 沙箱后端，隔离 Bash 会明确报错，文件工具可正常使用。
普通模式仍兼容无沙箱平台，但已有包装器返回错误时也会拒绝执行。

## 结果与清理

启动响应和最终响应/通知的 `worktree` 字段包含：

- `path`、`branch`、`base_head`：目录、唯一分支和起始提交。
- `changes.status`：Git porcelain 状态（包含未跟踪及忽略文件）。
- `changes.diff`：相对起始提交的文件状态列表，包括已提交的变化。
- `changes.head`、`changes.changed`：结束时 HEAD 和变化标记。
- `check_error`：状态检查失败原因。启动时 changes 尚未检查，不代表最终无修改。

成功、失败、取消和正常退出均保留已开始运行的 Worktree，不自动合并或删除。
尚未运行 Worker 的初始化失败会尝试回滚；不能证明安全时保留现场，并在错误中报告路径。
退出会取消并等待创建及运行中的任务，使用独立限时上下文收集变更，保存当前会话的完成通知。

父 Agent 可调用 `WorktreeCleanup({"task_id":"..."})`，按现有写操作权限审批。
清理仅接受本进程创建并已结束的任务，重新检查工作区、分支和所有文件状态；
有修改、暂存、未跟踪、忽略文件、新提交或隐藏索引条目时拒绝删除。没有 force 参数。
删除分支时校验其提交仍为起始 HEAD，并拒绝删除仍被其他 Worktree 使用的分支。

## 限制与恢复

- Git 仓库必须至少有一个提交；需要系统 Git。Git 创建和清理串行化，Worker 可并发运行。
- Git Worktree 共享对象库和引用，不能作为整个 Git 仓库的安全边界。因此隔离 Shell
  不开放共享 Git 管理目录写权限，`git add/commit` 等命令可能被拒绝；在父进程审查后处理提交。
- Shell 后端可读取工作区之外的文件，本功能限制文件工具路径和 Shell 写入，并非读取保密隔离。
- SIGKILL、断电等情况下不能保证完成通知或清理回滚，但不会主动删除工作成果。
  进程重启后任务表不会恢复，使用父会话中的路径和 `git worktree list --porcelain` 找回目录。
  审查文件与分支后，可手动使用不带 `--force` 的 `git worktree remove <路径>`；
  分支保留至确认成果已保存后再通过 `git branch -d <分支>` 删除。
  Git 本身可能删除忽略文件，手动移除前也要检查 `git status --ignored`。
- 安全检查假设没有外部进程同时修改待清理 Worktree。不要在清理期间由其他程序写入该目录。
- Windows/macOS 交叉编译不能代替各平台真实运行验证。
