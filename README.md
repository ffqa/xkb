# xkb (X Kanban)

`xkb` 是构建在 [`xissue`](https://github.com/ffqa/xissue) 执行底座之上的**全局需求拆解器 + 冲突感知调度器 + 独立交叉验收官**。

它解决的不是"让一个 Agent 写代码"，而是：把一个粗粒度需求拆成边界清晰的原子任务卡，在最多 5 个并发槽位内按优先级和依赖自动派发，用 **0 Token 的机械门禁** 做第一轮验收，再用**异构只读模型**做第二轮交叉审查，失败则唤醒原热会话增量修复，通过则自动合入并清理 Worktree。

```
需求/对话
   │  xkb-planner Skill（拆解 + 写卡）
   ▼
Spec ──> 原子任务卡（P0~P4 / depends_on / touches / verify_cmd）
   │  xkb 调度器（槽位、依赖、路径冲突锁）
   ▼
xissue（Worktree 隔离 + ACP Agent + 20 分钟热会话）
   │
   ▼
两阶段验收：机械门禁 + 异构只读 Review
   │            │
   │ 失败       └─ 通过 ──> merge_local ──> Done ──> Archived
   └─> 热会话增量修复（最多 2 次）──> 仍失败则 Blocked
```

---

## 目录

- [1. 项目定位与核心机制](#1-项目定位与核心机制)
  - [1.1 职责边界](#11-职责边界)
  - [1.2 看板状态机](#12-看板状态机)
  - [1.3 两阶段验收](#13-两阶段验收)
  - [1.4 Worktree 隔离与并发控制](#14-worktree-隔离与并发控制)
- [2. 源码编译与安装](#2-源码编译与安装)
- [3. 服务端常驻、五列 TUI 与 CLI](#3-服务端常驻五列-tui-与-cli)
  - [3.1 三种进程形态](#31-三种进程形态)
  - [3.2 五列终端看板](#32-五列终端看板)
  - [3.3 CLI 命令](#33-cli-命令)
  - [3.4 配置与数据目录](#34-配置与数据目录)
- [4. Skills 使用全景](#4-skills-使用全景)
  - [4.1 相关 Skill](#41-相关-skill)
  - [4.2 配合 xissue 的全自动闭环](#42-配合-xissue-的全自动闭环)
  - [4.3 不使用 xissue 的纯本地单兵用法](#43-不使用-xissue-的纯本地单兵用法)
- [5. 在 Devin / OpenCode / OMP 中挂载 MCP 与 Skill](#5-在-devin--opencode--omp-中挂载-mcp-与-skill)
  - [5.1 MCP 工具清单](#51-mcp-工具清单)
  - [5.2 一键注册](#52-一键注册)
  - [5.3 各 Agent 的 MCP 配置](#53-各-agent-的-mcp-配置)
  - [5.4 各 Agent 的 Skill 挂载](#54-各-agent-的-skill-挂载)
  - [5.5 自检](#55-自检)
- [6. 开发与发布](#6-开发与发布)
- [7. 常见问题](#7-常见问题)

---

## 1. 项目定位与核心机制

`xkb` 只做四件事：

1. **拆解**：把 Spec 落成 `P0~P4`、带 `depends_on`、`touches`、`verify_cmd` 的原子卡。
2. **调度**：全局最多 5 个槽位，按优先级、依赖满足、同项目路径不重叠自动补位。
3. **验收**：机械门禁（越界检查 + `verify_cmd` 实测）+ 异构只读 AI 交叉审查。
4. **归档**：Done 超量、超时或整个 Spec 完成时自动归档，并同步归档 `xissue` 记录。

### 1.1 职责边界

| 维度 | `xkb` | `xissue`（`127.0.0.1:7789`） | 明确不做 |
| :--- | :--- | :--- | :--- |
| 需求与规划 | 保存 `Specs` 和 `Backlog`，拆原子卡 | 不存 Backlog，只接收已派发的就绪任务 | 不在 xkb 内跑 `pm/tl/dev` 多角色聊天 |
| 并发与调度 | `Max=5`；优先级 + 依赖 + `touches` 冲突锁 | 管理子进程、内存预算、ACP Agent | 不同时推入 `touches` 重叠的两张卡 |
| 代码执行 | 生成 <500 字的极简 Prompt；按规模/优先级路由模型 | 创建 Git Worktree、启动 Agent、维持 20 分钟热会话 | 不让编码 Agent 自己流转看板或手动合并 |
| 审查与验收 | 阶段 1 机械门禁；阶段 2 异构只读 Reviewer；打回/合入决策 | 暴露 Diff/Worktree，`SendMessage` 唤醒热会话，`submit(mode=merge_local)` | 同模型自写自审；保留 Done 卡的 Worktree |
| 归档 | 三条归档规则 + 同步归档 xissue issue | 底层 SQLite 标记与日志 | 无限制堆积 Done |

卡片只有通过 `ValidateTodoGate` 才能从 `backlog` 进入 `todo`，全部是机械校验：

- `project_path` 必须是已存在的本地目录；
- `priority` 必须是 `P0~P4`，`size` 必须是 `small|large`；
- `touches` 非空，必须是相对路径，不能含 `..`，不能是仓库根 `.`；
- `verify_cmd` 非空，且能在 Worktree 内非交互执行；
- `goal`、`out_of_scope`、`acceptance` 非空，且不含 `TODO` / `<FILL_IN>` 占位符。

### 1.2 看板状态机

```text
backlog ──(门禁通过)──> todo ──(槽位/依赖/冲突均满足)──> running
   ▲                     │                                │
   └──────(撤回)─────────┘                                ▼
                                                       review
                                                          │
                    ┌──(FAIL 且 retry < 2：热会话修复)─────┤
                    │                                      ├──(FAIL 且 retry >= 2：熔断)──> blocked
                    ▼                                      ▼
                 running                                  done ──(超量/超时/Spec 全完)──> archived

blocked ──(xkb retry)──> todo
```

状态取值：`backlog | todo | running | review | done | blocked | archived`。

调度规则（每 2 秒一轮）：

- 活跃槽位按 `running + review` 计数，默认 `maxConcurrent: 5`；
- `todo` 按 `P0 -> P4`、再按创建时间排序；
- `depends_on` 中的全部卡片必须为 `done` 或 `archived`；
- 同 `project_path` 下，`touches` 有重叠的卡片不会同时进入活跃槽位；
- `ClaimTodo` 是原子认领，TUI、daemon、server 多进程不会重复派发同一张卡。

### 1.3 两阶段验收

**阶段 1：机械门禁（0 Token）**

在 `xissue` 为该 run 创建的 Worktree 内执行：

1. `CheckWorktreeViolations`：实际改动文件必须落在卡片的 `touches` 内；`AGENTS.md` 永远放行；越界文件直接判定失败并打回。
2. 在 Worktree 内执行 `verify_cmd`，默认超时 `verifyTimeoutSeconds: 180`；超时或非 0 退出即失败。

两项检查的证据合并写入一条 `review_runs` 记录（`stage=gate_cmd`）。

**阶段 2：异构只读 AI 审查**

- 只审 Diff，不审整个仓库；默认让编码 Agent 的**对侧**来审：
  - 编码用 `omp`，审查用 `devin/swe-2-high`；
  - 编码用 `devin`，审查用 `omp`。
- 第二轮及以后只审增量：`lastReviewedSHA..HEAD`，并把上一轮 findings 一起给 Reviewer。
- `devin` 没有只读开关，因此会在只读 HEAD 导出目录中运行；审查结束后比对真实 Worktree 与项目仓库的 `git status`，Reviewer 改动过工作区会被判失败。
- Reviewer 进程自身崩溃会直接 `blocked` 等人工处理，不消耗编码 Agent 的重试次数。

**免审白名单**（阶段 1 全绿后跳过阶段 2，直接合入）：

- 卡片 `security_sensitive == false` 且 `priority != P0`；
- 且满足其一：改动文件全部是 `.md`；或非文档改动文件 `<= 1` 个且总增删行数 `<= 10`。

**失败处理**

- 普通 FAIL：`SendMessage` 唤醒同一 `xissue` 热会话，状态回到 `running`，`retry_count + 1`；
- `retry_count >= 2`：取消 run，状态置 `blocked`，`last_error` 记录原因；
- Reviewer 只返回机械性 findings：唤醒热会话修复，下一轮只跑阶段 1；
- 合入失败：同样进入打回/熔断路径，并记录 `stage=merge` 的 FAIL 证据。

`review_runs` 是验收证据链：每轮记录 `stage`、`reviewer`、`base_sha`、`head_sha`、`verdict`、`findings`。`xkb show <card-id>` 可直接查看。

### 1.4 Worktree 隔离与并发控制

- **Worktree 由 `xissue` 创建**，通常在 `~/.xissue/runs/run-<id>/` 下；`xkb` 只负责找到它、提交未提交改动、取 Diff、跑验收、决定打回或合入。
- 验收前会把 Agent 留在工作区里的未提交改动提交为 `xkb round N`，这样二轮增量审查才能使用真实的 `lastReviewedSHA..HEAD` 范围。
- 当 `xissue` 旧版本返回的 `worktreePath` / `patch` 为空，或 patch 漏掉 untracked 文件时，`xkb` 会回退到本地 `git diff` 和 `git status`，保证门禁与 Reviewer 看到的是同一份真实改动。
- 并行只在**路径不重叠**时发生：同项目 `touches` 重叠的卡片串行；不同项目可并行。
- 通过后调用 `submit(mode=merge_local)` 合入目标分支并清理临时分支/Worktree；`done` 卡片不保留 Worktree。`merge_local` 只会写本地路径仓库：`xissue` 中该项目仓库的 `remote_url` 必须是本地目录（如 `/Users/you/src/app`）；GitHub/GitLab 远程仓库应改用 PR 或仅推送分支的模式，否则会报“直接合入仅支持本地路径仓库”。
- 归档规则（任一命中）：Done 超过 `doneMaxCount`（默认 20，归档最老的）、Done 停留超过 `doneTTLHours`（默认 24 小时）、同一 Spec 下全部子卡完成且最后一张完成满 1 小时。
- 同一时刻只允许一个引擎持有 `~/.xkb/engine.lock`。多个引擎长期共用一个库是危险的：验收状态是进程内状态，只有持锁进程会执行完整闭环。

---

## 2. 源码编译与安装

### 依赖

- Go **1.26+**（`go.mod` 为 `go 1.26.0`，`modernc.org/sqlite v1.61.1` 需要此版本）；
- `git`；
- 可选：`xissue` 常驻服务（不装也能用 xkb 做规划与看板，见 4.3）；
- 可选 Agent CLI：`omp`、`devin`、`opencode`，按你的模型路由配置安装。

### 源码编译

```bash
git clone https://github.com/ffqa/xkb.git
cd xkb

go build -trimpath -o bin/xkb ./cmd/xkb
./bin/xkb version
```

`bin/`、`dist/`、`*.db*` 已在 `.gitignore` 中。也可以直接 `go install ./cmd/xkb`，但常驻服务安装更推荐下面的稳定路径方式。

### 测试

```bash
gofmt -l .
go vet ./...
go test ./...

# 调度与存储的并发路径
go test -race ./internal/engine/ ./internal/store/
```

引擎测试使用假 `xissue` HTTP 服务 + 真实临时 Git 仓库，不需要启动真实 `xissue`。冒烟测试请使用临时 `XKB_HOME`，不要碰真实 `~/.xkb`：

```bash
export XKB_HOME=$(mktemp -d)
./bin/xkb list
```

### Homebrew

```bash
brew install ffqa/tap/xkb

# 由 Homebrew 托管常驻服务（等价于 xkb server --port 7790）
brew services start xkb
```

### 自更新

```bash
xkb update --check     # 只检查
xkb update             # 升级（Homebrew 安装走 brew upgrade，其余替换二进制）
xkb update --force     # 跳过 SHA256 校验
```

`autoUpdate` 默认开启，但只对持久安装生效（Homebrew 或 `~/.local/bin/xkb`），仓库内构建不会自动更新。检查间隔 1 小时，首次延迟 45 秒；升级后会重启自身。

### 安装为系统常驻服务

```bash
xkb service install --port 7790
xkb service status
xkb service logs
xkb service restart
xkb service uninstall
```

- macOS 使用 launchd：`dev.xpier.xkb`；
- Linux 使用 systemd user unit：`xkb.service`；
- 仓库构建的二进制会被复制到 `~/.local/bin/xkb` 再注册，避免服务指向会被覆盖的构建产物；
- 日志：`~/.xkb/logs/xkb.log`。

---

## 3. 服务端常驻、五列 TUI 与 CLI

### 3.1 三种进程形态

| 命令 | 调度器 | HTTP/SSE | 典型用途 |
| :--- | :--- | :--- | :--- |
| `xkb` | 持锁运行 | 否 | 直接在终端看板里规划、调度、验收 |
| `xkb daemon` | 持锁运行 | 否 | 无界面常驻（可配合另一进程只读看板） |
| `xkb server --port 7790` | 持锁运行 | 是 | 推荐常驻形态；给 TUI、CLI、外部集成提供只读 API |
| `xkb service install` | 由系统托管 `server` | 是 | 开机自启的正式部署 |

**引擎锁**：`~/.xkb/engine.lock`。只有持锁进程运行调度与验收。若 `server`/`daemon` 已持锁，再启动 `xkb` 会得到**只读 TUI**：`list/show/help` 可用，`add/ready/retry/archive/spec` 以及看板按键 `t/p/r/a` 被拒绝，避免两个进程同时写库、重复验收同一张卡。

HTTP API 是只读的：

| 方法 | 路径 | 说明 |
| :--- | :--- | :--- |
| `GET` | `/api/health` | `{"status":"ok","online":bool,"lastEvent":string}`，`online` 表示 `xissue` 是否可达 |
| `GET` | `/api/cards` | 全量卡片 JSON |
| `GET` | `/api/events` | SSE：`card_dispatched`、`card_reviewing`、`card_done`、`card_blocked` |

TUI 通过 SSE 立即刷新，并以 2 秒 tick 兜底；SSE 断开后 5 秒重连。CLI 的 `list` 会优先读服务器，`add/ready` 在服务器没有对应写路由时直接写 SQLite，对使用者透明。

### 3.2 五列终端看板

```bash
xkb
```

五列固定为 **Backlog / Todo / Running / Review / Done**；头部显示 `Slots: 活跃/上限`、`Blocked` 数、`Archived` 数、`xissue` 地址；底部状态行显示 xissue 在线状态、最近事件和操作结果。

| 按键 | 行为 |
| :--- | :--- |
| `h` / `←`、`l` / `→` | 切换列 |
| `j` / `↓`、`k` / `↑` | 在当前列选卡 |
| `Enter` / `Space` | 打开卡片详情（含 review 记录）；在详情/输出中用 `j/k`、`PgUp/PgDn` 滚动，`Esc/Enter/Space` 关闭 |
| `t` | Backlog ↔ Todo 切换；进入 Todo 前会跑 `ValidateTodoGate` |
| `p` | 循环优先级 `P0 -> P1 -> P2 -> P3 -> P4 -> P0` |
| `r` | 把所有 Blocked 卡片重置为 Todo（清零 retry 和上次验收 SHA） |
| `a` | 立即归档全部 Done 卡片 |
| `:` | 进入命令栏，支持 `list/show/add/ready/retry/archive/logs/cancel/spec new/help` |
| `q` / `Ctrl+C` | 退出 |

命令栏示例：`:list --status todo`、`:show xkb-001`、`:add --project ~/src/app ...`、`:ready xkb-001`、`:help`。

### 3.3 CLI 命令

| 命令 | 说明 |
| :--- | :--- |
| `xkb spec new --project <dir> --title <t> --file <spec.md>` | 保存 Spec，输出 `spec_id` |
| `xkb add --project <dir> --title <t> [flags]` | 创建 backlog 卡，输出卡 ID |
| `xkb ready <card-id>...` | 跑门禁并提升到 todo |
| `xkb list [--status todo,done] [--json]` | 列表；默认表格 |
| `xkb show <card-id> [--json]` | 卡片详情 + 每轮验收记录 |
| `xkb logs <card-id> [--tail N] [--json]` | 拉取当前 run 的 `xissue` 事件日志 |
| `xkb retry <card-id>...` | Blocked -> Todo，清零 retry、`lastReviewedSHA`、`xissue_run_id` |
| `xkb cancel <card-id>` | 取消 `xissue` run，卡片置 Blocked（原因“已手动取消”） |
| `xkb archive [--all-done]` | 按三条规则归档 Done，或立即归档全部 Done |
| `xkb mcp [--install]` | stdio MCP 服务；带 `--install` 注册 Devin/OpenCode |
| `xkb update [--check] [--force]` | 自更新 |
| `xkb version` | 版本与平台 |

`xkb add` 的完整参数：

```bash
xkb add \
  --project "$PWD" \
  --title "为订单接口增加幂等键" \
  --priority P1 \
  --size small \
  --touches internal/order/,internal/api/order.go \
  --verify "go test -count=1 ./internal/order/..." \
  --goal "同一 idempotency-key 重复提交返回同一订单，不新建记录" \
  --scope "不修改数据库表结构，不改网关" \
  --accept "单测覆盖重复提交与并发提交；接口返回 200 且订单 ID 相同" \
  --depends xkb-003 \
  --spec spec-20261101-01 \
  --security
```

说明：

- `--touches`、`--depends` 都是逗号分隔；
- `--goal` 为空时可用 `--file <markdown>` 读取目标正文；
- `--security` 会把卡标记为 `security_sensitive`，强制进入 AI Review；
- 创建后仍是 `backlog`，必须 `xkb ready <id>` 才能进 `todo`（MCP 的 `auto_ready` 等价于创建后立即过门禁）。

### 3.4 配置与数据目录

配置：`~/.xkb/config.yaml`（不存在时按默认值生成，权限 0600）。`XKB_HOME` 可覆盖整个数据目录，测试和 dogfood 环境务必使用独立的 `XKB_HOME`。

```yaml
dbPath: /Users/you/.xkb/xkb.db
maxConcurrent: 5
doneMaxCount: 20
doneTTLHours: 24

xissueURL: http://127.0.0.1:7789
xissueToken: ""            # 留空则从 ~/.xissue/config.yaml 读取 token
xissueDataDir: /Users/you/.xissue

specAgent:
  agent: devin
  model: swe-2-high
smallExecAgent:
  agent: omp
  model: ""
largeExecAgent:
  agent: devin
  model: swe-2-high

verifyTimeoutSeconds: 180
reviewTimeoutSeconds: 600
runTimeoutMinutes: 30

autoUpdate: true
serverPort: 7790
serverURL: http://127.0.0.1:7790
```

| 字段 | 默认 | 说明 |
| :--- | :--- | :--- |
| `dbPath` | `~/.xkb/xkb.db` | SQLite 单文件 |
| `maxConcurrent` | `5` | 全局活跃槽位（running + review） |
| `doneMaxCount` | `20` | Done 超过此数归档最老的 |
| `doneTTLHours` | `24` | Done 停留超时归档 |
| `xissueURL` | `http://127.0.0.1:7789` | 保持默认值时，会用 `~/.xissue/config.yaml` 的 `listen` 覆盖 |
| `xissueToken` | 空 | 为空时读取 `~/.xissue/config.yaml` 的 `token` |
| `xissueDataDir` | `~/.xissue` | 查找 run 与 Worktree 的根目录 |
| `specAgent` | `devin/swe-2-high` | 拆卡阶段使用的 Agent |
| `smallExecAgent` | `omp` | `size=small` 且非 P0/P1 的编码 Agent |
| `largeExecAgent` | `devin/swe-2-high` | `size=large` 或 P0/P1 的编码 Agent |
| `verifyTimeoutSeconds` | `180` | `verify_cmd` 超时 |
| `reviewTimeoutSeconds` | `600` | AI Review 超时 |
| `runTimeoutMinutes` | `30` | 单 run 超时，超时视为死 run |
| `autoUpdate` | `true` | 仅持久安装生效 |
| `serverPort` / `serverURL` | `7790` / `http://127.0.0.1:7790` | CLI 探测健康检查的地址 |

数据：单文件 SQLite，开启 `WAL` 和 `busy_timeout=5000`，TUI / daemon / server / `xkb mcp` 可并发读写。三张表：

- `specs`：需求文档与关联项目；
- `cards`：卡片全字段，包括 `touches_json`、`depends_on_json`、`verify_cmd`、`last_reviewed_sha`、`retry_count`、`xissue_run_id` 等；
- `review_runs`：每轮验收证据。

---

## 4. Skills 使用全景

这里说的 Skill 是 **Agent Skill**：`<skills-root>/<name>/SKILL.md`，带 `name` 和 `description` frontmatter，Agent 在系统提示里看到元数据，按需读取完整正文。xkb 仓库本身不打包 Skill 文件；以下 Skill 名来自团队技能库，按第 5 节挂载到对应 Agent 即可。

### 4.1 相关 Skill

| Skill | 用途 | 典型触发 |
| :--- | :--- | :--- |
| `xkb-planner` | 需求分析、Spec 沉淀、原子卡拆解，直接通过 MCP 写入看板 | “把这个需求拆成任务卡”、“录入 xkb” |
| `xkb-project-workflow` | 开发 xkb 本体：构建、测试、包地图、坑位 | 修改 `internal/engine`、跑 `go test`、问 xkb 结构 |
| `xkb-self-dogfooding` | 用 xkb 自己的调度器开发 xkb：MCP 写卡、daemon、Reviewer/Diff 陷阱、阻塞卡复验 | “dogfood”、“用 xkb 自己跑” |

`xkb-planner` 的硬约束与看板门禁一致：`touches` 必须是相对路径、每张卡必须自包含 `verify_cmd`、禁止 `TODO` / `<FILL_IN>`、禁止为同一文件拆两个无依赖并行卡。它通过两个 MCP 工具落地：

```json
// xkb_create_spec
{"project_path":"/abs/path/to/repo","title":"订单幂等","body":"# 背景 ..."}

// xkb_create_cards（auto_ready 会在门禁通过后直接进入 todo）
{
  "spec_id":"spec-20261101-01",
  "project_path":"/abs/path/to/repo",
  "cards":[{
    "title":"订单接口支持幂等键",
    "priority":"P1",
    "size":"small",
    "touches":["internal/order/","internal/api/order.go"],
    "verify_cmd":"go test -count=1 ./internal/order/...",
    "goal":"重复 idempotency-key 返回同一订单",
    "out_of_scope":"不改表结构、不改网关",
    "acceptance":"单测覆盖重复/并发提交，订单 ID 一致",
    "auto_ready":true
  }]
}
```

### 4.2 配合 xissue 的全自动闭环

前提：

1. `xissue` 已在 `127.0.0.1:7789` 运行，token 可被 xkb 从 `~/.xissue/config.yaml` 读取；
2. 本机已安装编码 Agent CLI（默认 `omp` 和 `devin`），且能在非交互模式运行；
3. 有一个持锁的引擎进程：`xkb server`、`xkb daemon` 或 `xkb service install` 任选其一；
4. 目标仓库是干净的 Git 仓库（有未提交改动时 `merge_local` 可能失败）。

闭环步骤：

1. **拆解**：在 OMP/Devin/OpenCode 中调用 `xkb-planner`，产出 1 个 Spec + 3~7 张原子卡。要并行就拆不同文件；同文件改动用 `depends_on` 串成链。
2. **入列**：卡片 `auto_ready` 直接进 `todo`；未设置则 `xkb ready <id>...`。门禁失败会给出具体原因，不会进入调度。
3. **派发**：调度器每 2 秒扫描，选优先级最高、依赖满足、无路径冲突的卡，原子认领后调用 `xissue`：
   - 按配置注册/查找项目与仓库；
   - 创建 issue（同一卡失败重试时复用，不重复建 issue）；
   - 用 <500 字 Prompt（目标、禁区、`verify_cmd`、`touches`）启动 ACP run；
   - `xissue` 创建 Worktree 并运行 Agent。
4. **验收**：run `completed` 后进入 `review`，`VerifyCard` 跑阶段 1 + 阶段 2。
5. **修复**：FAIL 时向同一热会话发消息，状态回 `running`，最多 2 次；超过则 `blocked`，需要人看 `xkb show` / `xkb logs` 后决定 `xkb retry` 或 `xkb cancel`。
6. **合入与归档**：通过后 `merge_local` 合入目标分支、删临时分支与 Worktree、置 `done`；随后立即触发一次调度和归档，下游卡片可在同一轮被拉起。

日常运维命令：

```bash
xkb list --status running,review
xkb show xkb-004 --json
xkb logs xkb-004 --tail 100
xkb retry xkb-004
xkb archive --all-done
```

选型建议：拆卡用强模型；边界清晰的小卡用低成本模型；P0/P1 或跨模块契约用强模型；默认配置下 Reviewer 取编码者的对侧。这样 80% 的卡花小钱，风险卡花在刀刃上。

### 4.3 不使用 xissue 的纯本地单兵用法

没有 `xissue` 时，xkb 仍是一个可用的**规划、拆解、门禁、看板**工具，但**没有**自动执行、Worktree 隔离、热会话修复和两阶段自动验收。

可用：

- `xkb spec new` 沉淀 Spec；
- `xkb add` + `xkb ready` 维护 `backlog -> todo`，享受五要素门禁；
- `xkb list` / `xkb show` 查看卡片与优先级；
- TUI 五列看板、优先级调整、Blocked 重试、Done 归档（注意：没有其他引擎持锁时，`xkb` 自己会持锁并启动调度器，同样会周期性尝试派发；无 `xissue` 时只是记录派发失败，卡片仍留在 `todo`。纯本地规划建议直接用 CLI/MCP，避免日志噪音）；
- MCP：`xkb_create_spec`、`xkb_create_cards`、`xkb_promote_todo`、`xkb_board_status`；
- `xkb-planner` 仍可把需求拆成严格契约的卡片，交给你自己或本地 Agent 执行。

不可用/不要期待：

- `todo` 不会自动变成 `running`。没有 `xissue` 时派发会失败，卡片留在 `todo`，`last_error` 记录连接/注册错误；
- 没有自动 Worktree、没有 `touches` 越界机械检查、没有 `verify_cmd` 自动执行、没有异构 Review、没有自动合入；
- 因此不要在没有 `xissue` 的情况下启动 daemon 期待“自动干活”。

推荐的纯本地节奏（单人或单 Agent）：

```bash
# 1. 规划：用 xkb-planner 或手写 Spec / 卡片
xkb spec new --project "$PWD" --title "重构配置加载" --file spec.md
xkb add --project "$PWD" --title "拆分 config 包" \
  --touches internal/config/ \
  --verify "go test -count=1 ./internal/config/..." \
  --goal "把配置加载拆成独立包" \
  --scope "不改配置格式与默认值" \
  --accept "go test 通过，默认配置不变"
xkb ready xkb-001

# 2. 执行：自己（或本地 Agent）在独立分支/自己创建的 worktree 中实现
git switch -c card/xkb-001
# ... 编码 ...

# 3. 验收：手动复现卡片契约
git diff --stat
go test -count=1 ./internal/config/...

# 4. 记录完成（xkb 没有 done 子命令，本地单人模式直接用 SQLite 作为逃生口）
#    执行前确认没有 daemon/server 持锁
sqlite3 ~/.xkb/xkb.db \
  "update cards set status='done', finished_at=CURRENT_TIMESTAMP, last_error='' where id='xkb-001';"

# 5. 清理
xkb archive --all-done
```

注意：

- 不要在 daemon/server 持锁时直接改 SQLite 状态，会与调度器的视图竞争；先停引擎，或用只读 TUI 观察。
- 本地单人模式没有自动的 `touches` 越界检查，拆卡时仍应把 `touches` 写小、写准，方便 Code Review。
- 想恢复全自动，只需启动 `xissue` 和 `xkb server`；已有卡片、依赖、验收命令都可以直接复用。

---

## 5. 在 Devin / OpenCode / OMP 中挂载 MCP 与 Skill

### 5.1 MCP 工具清单

`xkb mcp` 是 stdio JSON-RPC 2.0 服务，不监听端口，直接读写同一个 SQLite（WAL）。它不需要引擎锁，因此可以和 server/TUI 并存。

| 工具 | 必填 | 说明 |
| :--- | :--- | :--- |
| `xkb_create_spec` | `project_path`、`title`、`body` | 保存 Spec，返回 `spec_id`；可选 `xissue_proj` |
| `xkb_create_cards` | `project_path`、`cards` | 批量建卡；`spec_id` 可选；每张卡 `title` 必填，门禁字段不全会拒绝；`auto_ready` 在门禁通过后直接进 `todo` |
| `xkb_promote_todo` | `ids` | 对 backlog 卡跑门禁并提升到 `todo` |
| `xkb_board_status` | 无 | 各列卡片、阻塞原因、Spec 完成度 |

没有“标记 Done”或“合入”的 MCP 工具：自动流程中 Done 只能由 `xissue` 验收通过后产生，避免 Agent 自审自过。

### 5.2 一键注册

```bash
xkb mcp --install
```

会写两个文件（存在则合并，保留其他 server）：

- `~/.config/opencode/opencode.json` 的 `mcp.xkb`（OpenCode local 形式）；
- `~/.config/devin/mcp_config.json` 的 `mcpServers.xkb`。

OMP 不在自动注册范围内，需要手动加 `~/.omp/agent/mcp.json`；不过 OMP 会自动导入 OpenCode 的 `mcp` 配置，所以完成上面的安装后，OMP 通常也能直接看到 `xkb`。

### 5.3 各 Agent 的 MCP 配置

把路径换成你的真实二进制：Homebrew 通常是 `/opt/homebrew/bin/xkb`，`xkb service install` 后是 `~/.local/bin/xkb`，仓库构建是 `<repo>/bin/xkb`。MCP 客户端不会展开 `~`，请写绝对路径。

**Devin**：`~/.config/devin/mcp_config.json`

```json
{
  "mcpServers": {
    "xkb": {
      "command": "/Users/you/.local/bin/xkb",
      "args": ["mcp"]
    }
  }
}
```

**OpenCode**：`~/.config/opencode/opencode.json`

```json
{
  "mcp": {
    "xkb": {
      "type": "local",
      "command": ["/Users/you/.local/bin/xkb", "mcp"],
      "enabled": true
    }
  }
}
```

**OMP**：`~/.omp/agent/mcp.json`（项目级用 `<repo>/.omp/mcp.json`）

```json
{
  "mcpServers": {
    "xkb": {
      "type": "stdio",
      "command": "/Users/you/.local/bin/xkb",
      "args": ["mcp"]
    }
  }
}
```

OMP 也支持项目根目录的 `mcp.json` / `.mcp.json` 作为可移植回退；用 profile 时用户级路径变为 `~/.omp/profiles/<name>/agent/mcp.json`。

### 5.4 各 Agent 的 Skill 挂载

挂载时保持 `<skills-root>/<name>/SKILL.md` 结构：OMP 的常规技能目录只扫描一层，嵌套分组不会被发现；其他 Agent 也按各自的单层约定处理最稳。`SKILL.md` 至少要有：

```markdown
---
name: xkb-planner
description: 需求分析与原子任务拆解，通过 xkb MCP 写入看板
---

# xkb-planner
...
```

挂载位置：

| Agent | Skill 根目录 | MCP 配置 |
| :--- | :--- | :--- |
| Devin | `~/.config/devin/skills/` | `~/.config/devin/mcp_config.json` |
| OpenCode | `~/.config/opencode/skills/` | `~/.config/opencode/opencode.json` |
| OMP | `~/.omp/agent/skills/` | `~/.omp/agent/mcp.json` |

从团队技能库复制（把 `<skills-src>` 换成实际目录）：

```bash
# Devin
mkdir -p ~/.config/devin/skills
cp -R <skills-src>/xkb-planner ~/.config/devin/skills/

# OpenCode
mkdir -p ~/.config/opencode/skills
cp -R <skills-src>/xkb-planner ~/.config/opencode/skills/

# OMP
mkdir -p ~/.omp/agent/skills
cp -R <skills-src>/xkb-planner ~/.omp/agent/skills/
```

开发 xkb 本体时，再按需挂载 `xkb-project-workflow`；用 xkb 跑自己的开发流程时挂载 `xkb-self-dogfooding`。三个 Skill 都与 `xkb-planner` 同级复制即可。OMP 还支持 `omp skill install` 从 Skillshare 安装，以及 `~/.omp/agent/managed-skills/` 下的自动学习技能；手工放置的用户技能优先级更高。

项目级作用域：

- OpenCode / Devin 的 Skill 目录按各自工具的约定放置；
- OMP 支持项目级 `.omp/skills/`（随项目分发）或 `skills.customDirectories` 指向其他根目录；
- MCP 项目级配置：OMP 用 `.omp/mcp.json`，OpenCode 用项目 `opencode.json` / `.opencode/`，避免每个仓库重复配置全局服务。

### 5.5 自检

MCP 握手与工具枚举（不需要 xissue、不会写卡片）：

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | xkb mcp
```

应看到 `serverInfo.name = "xkb"` 和四个工具。若 Agent 里看不到工具：

1. 确认 `command` 是绝对路径且可执行：`/path/to/xkb version`；
2. 确认没有把 `args` 写成字符串；
3. 重启 Agent（MCP 在启动时发现）；
4. OMP 用 `/mcp list` 查看服务状态；
5. `xkb mcp --install` 只覆盖 `xkb` 键，不会删除其他 server，但会重写 JSON 格式，建议先备份。

Skill 自检：在 Agent 中输入“把当前需求拆成 xkb 任务卡”，应命中 `xkb-planner`；未命中时检查目录层级、`description` 是否为空、frontmatter 是否合法。

---

## 6. 开发与发布

### 包结构

| 包 | 职责 |
| :--- | :--- |
| `internal/config` | `~/.xkb/config.yaml` 默认值；`xissue` listen/token 回退 |
| `internal/store` | WAL SQLite；`ClaimTodo` 原子认领 |
| `internal/gate` | `ValidateTodoGate`、`PathsOverlap`、`CheckWorktreeViolations` |
| `internal/xissue` | HTTP 桥接；缺少 worktree/patch 时的本地 git 兜底 |
| `internal/router` | P0/P1 或 large -> 强模型，否则小模型；Reviewer 取对侧 |
| `internal/reviewer` | 只读 CLI 审查、白名单、mechanical-only、篡改检测 |
| `internal/engine` | Tick 调度、两阶段验收、三条归档规则 |
| `internal/mcp` | stdio JSON-RPC 工具 + 配置安装器 |
| `internal/tui` | 五列 BubbleTea 看板 |
| `internal/server` | 只读 HTTP + SSE |
| `internal/updater` | GitHub Release 检查与版本比较 |
| `cmd/xkb` | 子命令、服务安装、自更新、TUI 入口 |

### 关键不变量

- 卡片进入 `todo` 必须经过 `ValidateTodoGate`；
- `ClaimTodo` 必须先于任何 `xissue` 调用，派发失败要释放回 `todo`；
- 调度器把 `running + review` 都算作活跃槽位，同一 tick 内已选中的卡也参与冲突判断；
- 不做只验证 wiring 的测试；引擎测试用假 `xissue` + 临时真实仓库；
- 兄弟仓库 `../xissue` 可能被其他人同时修改，联调前先看 `git status` 与文件时间；
- 不要覆盖正在运行的二进制，构建带版本号的新路径（如 `bin/xkb_next1`）后再切换服务。

### 发布

```bash
git tag v0.1.6
git push origin v0.1.6
```

GitHub Actions 会：

1. 跑 `go test ./...`；
2. 交叉编译 `darwin/arm64`、`darwin/amd64`、`linux/amd64`、`linux/arm64`，注入版本号；
3. 生成 `SHA256SUMS.txt` 并创建 GitHub Release；
4. 配置了 `TAP_GITHUB_TOKEN` 时，用 `scripts/brew-publish.sh` 更新 `ffqa/homebrew-tap`。

手动发 Homebrew 公式：

```bash
bash scripts/brew-publish.sh v0.1.6
```

---

## 7. 常见问题

**`engine.lock 已被占用`**

同一时刻只能有一个持锁引擎。先找占用者：

```bash
lsof ~/.xkb/engine.lock
pgrep -fl xkb
```

停掉旧的 `xkb` / `xkb daemon` / `xkb server` / 服务后再启动。不要删除锁文件来绕过；锁文件由持锁进程管理。

**TUI 显示 `[READ-ONLY]`**

说明另一个进程（通常是 `xkb server` 或 `xkb daemon`）持有引擎锁。这是预期行为：看板可读，写操作被拒绝，避免双写。要操作看板就停掉常驻服务，或继续用 CLI/MCP。

**`xissue` 显示 offline / 卡片停在 todo**

- 检查 `127.0.0.1:7789` 是否在运行；
- 检查 `xissueURL` 与 token；token 为空时会读 `~/.xissue/config.yaml`；
- 派发阶段的连接错误是瞬时的，不消耗 retry，卡片保留在 `todo`；
- 如果 `last_error` 提示项目未注册，确认 `project_path` 是真实 Git 仓库且 `xissue` 可访问该路径。

**门禁拒绝卡片**

按报错补字段：`priority`、`size`、`touches`、`verify_cmd`、`goal`、`out_of_scope`、`acceptance`。占位符 `TODO` / `<FILL_IN>` 也会被拒绝；`touches` 不能是绝对路径、不能含 `..`、不能是仓库根。

**卡片 Blocked**

```bash
xkb show <id>            # 看 gate_cmd / ai_review / merge 的 verdict 和 findings
xkb logs <id> --tail 100 # 看 xissue run 日志
xkb retry <id>           # 确认可重试后重置到 todo
```

审查进程崩溃、热会话唤醒失败、连续 2 次验收失败都会直接 Blocked，不自动重试。

**端口冲突 / CLI 读不到 server**

`server --port` 与 `config.yaml` 的 `serverURL` 必须一致；只改其中一个会导致 CLI 健康探测失败并回退 SQLite。默认端口 `7790`。

**合入失败：`直接合入仅支持本地路径仓库`**

`xkb` 走的是 `xissue` 的 `merge_local`，只写本地路径仓库。若 `xissue` 中该项目的仓库注册成了 `https://github.com/...` 这类远程地址，合入一定失败，修改会保留在工作区。处理方式：

```bash
sqlite3 ~/.xissue/xissue.db \
  "select id,project_id,name,provider,remote_url from repos;"
```

- 期望：`xkb` 项目的 `remote_url` 是本地仓库绝对路径，`provider` 为 `plain`；
- 若是 GitHub/GitLab 远程：改用 PR 或“仅推送分支”的提交模式，不要用 `merge_local`；
- 修正项目仓库指向后重新提交该卡（`xkb retry <id>` 或按验收流程重试），不要手工直接改目标分支。

**Worktree 残留**

正常合入会清理。异常中断后先看 `~/.xissue/runs/run-<id>/` 和 `git worktree list`，确认没有引擎在跑再人工清理；不要删除活跃 run 的目录。

**数据库 busy**

已开启 WAL 和 5 秒 busy timeout；持续 busy 通常意味着某个进程持锁时间过长（例如超大 `verify_cmd`），先查 `sqlite3 ~/.xkb/xkb.db "pragma wal_checkpoint;"` 与正在跑的测试。

**测试污染真实数据**

所有冒烟/集成测试使用 `export XKB_HOME=$(mktemp -d)`，并确认 `config.yaml` 的 `dbPath` 指向临时目录。

---

## License

MIT，见 [LICENSE](./LICENSE)。

设计与开发任务拆解见 [SPEC.md](./SPEC.md) 与 [TASKS.md](./TASKS.md)。
