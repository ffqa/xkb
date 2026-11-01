---
name: xkb-project-workflow
version: 1.0.0
description: "开发与验证 xkb Go 看板本体：构建/测试/冒烟命令、包地图、关键不变量与 TUI 渲染陷阱。修改 internal/ 或 cmd/ 前先读。"
metadata:
  requires:
    bins: ["git", "go"]
---

# xkb Project Workflow

仓库：xkb 自身（Go module `github.com/xpier/xkb`，`go 1.26.0`）。以下命令都在仓库根目录执行。

## 改动前后都要跑

```bash
gofmt -w . && go vet ./... && go test ./...
go test -race ./internal/engine/ ./internal/store/
go build -o bin/xkb ./cmd/xkb
export XKB_HOME=$(mktemp -d)   # 冒烟绝不碰真实 ~/.xkb
./bin/xkb list
```

MCP 冒烟（行分隔 JSON-RPC，不写卡片）：

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize"}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | ./bin/xkb mcp
```

TUI 冒烟（需要 PTY）：`printf q | script -q /dev/null ./bin/xkb`。

强制彩色输出：宿主环境常见 `NO_COLOR=1` / `TERM=dumb`，lipgloss 会退化成 Ascii profile，抓帧里没有 SGR。要验证强调色、dim、reverse：

```bash
go build -o /tmp/xkb-smoke ./cmd/xkb
export XKB_HOME=$(mktemp -d)
/tmp/xkb-smoke add --title t --project /tmp/p --touches 'a/' --verify true >/dev/null
(sleep 1; printf l; sleep 1; printf q) | env -u NO_COLOR TERM=xterm-256color CLICOLOR_FORCE=1 \
  script -q /tmp/frame.txt /tmp/xkb-smoke >/dev/null 2>&1
grep -o $'\033\[[0-9;]*m' /tmp/frame.txt | sort | uniq -c
```

Go 测试里**不要**用 `t.Setenv("CLICOLOR_FORCE", "1")`：lipgloss 用 `sync.Once` 缓存默认 renderer 的 profile，结果依赖测试顺序。断言 `lipgloss.Style` 的 getter（`GetBorderTopForeground`、`GetFaint`、`GetBold`）加纯文本标记，布局适配用 `lipgloss.Width(line) <= width` 覆盖多个终端尺寸（40x10、80x24、120x30、各 `m.col`）。

## 包地图

| Package | 职责 |
|---|---|
| internal/config | `~/.xkb/config.yaml` 默认值、xissue listen/token 回退 |
| internal/store | WAL SQLite；`ClaimTodo` 是 TUI/daemon 共享的原子 todo->running 认领 |
| internal/gate | `ValidateTodoGate`、`PathsOverlap`（保守 glob）、`CheckWorktreeViolations` |
| internal/xissue | HTTP 桥接；`worktreePath`/`patch` 缺失时回退本地 git diff |
| internal/router | P0/P1 或 large -> devin，否则 omp；reviewer 取对侧 |
| internal/reviewer | 只读 CLI 审查、`OPENCODE_PERMISSION`、git-status 篡改检测、白名单、mechanical-only 判定 |
| internal/engine | Tick（槽位/依赖/冲突/认领）、两阶段 VerifyCard、Archiver 三条规则 |
| internal/mcp | stdio JSON-RPC 工具 + 配置安装器 |
| internal/server | 只读 HTTP + SSE |
| internal/updater | GitHub Release 检查与版本比较 |
| internal/tui | 五列 BubbleTea 看板，每 2s tick |
| cmd/xkb | 子命令、服务安装、自更新、TUI 入口 |

## 不变量与坑位

- 卡片只能经 `gate.ValidateTodoGate` 进入 `todo`；`touches` 必须相对、无 `..`、非空、不是仓库根。
- 派发时 `ClaimTodo` 必须先于 xissue 调用；失败要把卡释放回 `todo`。
- 调度器把 `running + review` 都算作活跃槽位，同一 tick 内已选中的卡也参与冲突判断。
- mechanical-only AI 结论：走热会话重试；下一轮跳过 AI（`skipAIReview` 检查最新 `ai_review` 是否为 FAIL）。
- 不要写只回显 wiring 的测试；引擎测试用假 xissue HTTP server + 真实临时 git 仓库。
- `go.mod` 是 `go 1.26.0`，因为 `modernc.org/sqlite v1.61.1` 要求 Go 1.26；旧工具链会自动下载。
- 兄弟仓库 `../xissue` 可能被人类/其他 Agent 同时修改。动手前看 `git status` 和文件 mtime；xkb 要同时兼容新旧 `/changes` 响应。
- 不要覆盖正在运行的二进制：构建带版本号的新路径（`bin/xkb_next1`），停服务后再切换。

## TUI 列装饰

- 活跃列是 `m.col`：`activeColumnStyle`（accent `lipgloss.Color("6")` 边框）+ `activeTitleStyle`（accent 加粗），标题前缀 `"▸ "`；其他标题用 `dimStyle`。
- 选中卡片保持整行 `selectedStyle` reverse；`styledLine` 会补齐到内容宽度，所以选中行正好是 `width` 个 cell。
- `columnsView` 计算 `contentW = colOuter - 4`；重排版式时不要改这个值，否则 40 列终端溢出。

## 破坏性卡片操作（archive / delete）

- `xkb archive <id>...` 把任意状态置为 `archived`；无参数按引擎规则，`--all-done` 归档所有 done 卡。`xkb delete <id>...` 经 `store.DeleteCard` 在一个事务里删卡 + `review_runs`（并返回是否存在）。
- 两者都必须先停掉在飞的 xissue run：状态是 `running`/`review` 且 `XIssueRunID != 0` 时 best-effort 调 `CancelRun`——打印错误，但绝不中断本地变更。`Archive` 另外 best-effort 调 `ArchiveIssue`。
- 这段逻辑收敛在一个共享 helper（`cancelActiveRun`）；跳过它去删卡，引擎会继续写已删除的行，Reviewer 会打回。
