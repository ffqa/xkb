---
name: xkb-self-dogfooding
version: 1.0.0
description: "用 xkb 自己的调度器开发 xkb：MCP 写卡、与陈旧 TUI 并存跑 daemon、修 Reviewer/Diff 陷阱、不重跑 Agent 复验阻塞卡。"
metadata:
  requires:
    bins: ["git", "go"]
---

# xkb self-dogfooding

适用场景：用 xkb 自己的调度链路（MCP 写卡 + 持锁引擎 + 真实 omp/devin）开发 xkb 本体。

## 预检

1. `git status --short` 必须干净；先把本地修改提交，否则 xissue 的 `merge_local` 可能失败。
2. 查正在跑的引擎：`pgrep -fl 'bin/xkb'`、`lsof ~/.xkb/engine.lock`。
3. **绝不覆盖正在运行的二进制**。按版本号构建：`go build -o bin/xkb_nextN ./cmd/xkb`；先停服务再切路径。
4. 清掉泄漏的环境变量：`unset XKB_HOME`，并用 `sqlite3 ~/.xkb/xkb.db "select id,status,title from cards"` 确认目标 DB。

## 写卡

- 用 `xkb mcp` 的 stdio JSON-RPC（`initialize` 后 `tools/call`）。`depends_on` 引用前面的真实 ID 时，一张一张创建。
- 门禁必填：`project_path`、`title`、`priority`、`size`、`touches`、`verify_cmd`、`goal`、`out_of_scope`、`acceptance`，加 `auto_ready: true`。
- 用 `xkb list` 和 `xkb show <id> --json` 核对 ID 与依赖。

## 与陈旧 TUI 并存

- 用户的 TUI 持着 `~/.xkb/engine.lock` 却不派发时：别杀它，另起一个独立 home 的 daemon，配置指向真实 DB：
  `~/.xkb-dogfood/config.yaml` 里写 `dbPath: ~/.xkb/xkb.db`；启动 `XKB_HOME=~/.xkb-dogfood ./bin/xkb_nextN daemon`。
- `ClaimTodo` 保证不会重复派发；但验收状态只存在进程内，长期不要两个引擎并存。

## Reviewer 陷阱

- Devin 参数：`devin --permission-mode auto --respect-workspace-trust false --model <model> -p <prompt>`；prompt 必须是 `-p` 的值本身，否则 devin 会把它当 PATH。
- prompt 必须显式禁用工具：“禁止调用任何工具（read/grep/exec/edit/write 等），不要检查工作区……只输出 JSON”；否则 devin 返回进度散文而不是 JSON。
- Devin 在只读的 HEAD 导出里跑；事后用真实 worktree 和项目仓库的 HEAD+status 做对比。

## Diff 陷阱

- xissue 的 `changes?patch=1` 可能返回非空 patch 却漏掉未跟踪文件（旧 daemon）。所以在 `VerifyCard` 里 `CommitWorktree` 之后，用 `xissue.ChangedStats` 重算 files/lines、用 `xissue.GitDiff` 重算 diff，门禁和 Reviewer 都用这份结果。

## 不重跑 Agent 复验阻塞卡

1. 停掉 dogfood daemon。
2. `sqlite3 ~/.xkb/xkb.db "update cards set status='review', last_error='' where id='xkb-00N';"`，保留 `xissue_run_id`。
3. 用修好的版本化二进制启动 daemon；它会在已提交的 worktree 上重跑门禁 + AI Review。

## 验收

- `xkb show <id> --json`：`status`、`retryCount`、`lastError`、`reviews`（`gate_cmd` / `ai_review` 的 verdict）。
- `git -C ~/.xissue/runs/run-N/<repo> log --oneline` 应出现 `xkb round N`；worktree status 干净。
- `xkb list` 里卡片推进到 done，下游 `depends_on` 卡在下一个 tick 派发。
