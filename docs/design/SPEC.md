# xkb (X Kanban) 架构与实现规范 (SPEC v1.0)

## 1. 目标与定位

`xkb` 是建立在 [`xissue`](../xissue/README.md) 执行底座之上的 **全局需求拆解器（Spec Splitter）+ 冲突感知调度器（Conflict-Aware Scheduler）+ 独立交叉验收官（Two-Stage Verifier）**。

设计目标：在仅使用平价/中量级模型（如 `omp (with opencode)` + `devin (swe-2-high)`）的前提下，通过**微卡拆分（2~5 分钟/≤2 文件/≤120 行）、0 Token 机械硬门禁、异构模型只读交叉审查、热会话增量修复**，以最低 Token 消耗达到高质量自动化并行交付。拆解必须足够细：大卡会让单个槽位长时间被占用，也会放大审查与返工成本。

### 1.1 职责边界（做什么 vs 不做什么）

| 维度 | `xkb` 负责（本项目） | `xissue` 负责（底层常驻服务 `127.0.0.1:7789`） | 明确不做（禁区） |
| :--- | :--- | :--- | :--- |
| **需求与规划** | 维护全局 `Specs` 与 `Backlog` 蓄水池；将大需求拆解为带 `P0~P4`、`depends_on`、`touches`、`verify_cmd` 的原子任务卡。 | 不存 `Backlog`，只接收 `xkb` 确认派发的就绪任务。 | 严禁在 `xkb` 中做多角色常驻聊天（不搞 `pm/tl/dev` 传话筒）。 |
| **并发与调度** | 全局 `Max=5` 槽位控制；按 `P0 -> P4` 优先级、前置依赖（`depends_on` 已完成）、同项目路径无重叠（`touches` 冲突锁）自动从 `Todo` 补位派发。 | 管理子进程生命周期与内存预算。 | 严禁将同项目 `touches` 重叠的两张卡同时推入 `Running`。 |
| **代码执行** | 生成极简执行 Prompt（`< 500` 字：仅含本卡目标、禁区、验收命令），按任务规模/优先级路由模型。 | 创建 Git Worktree、启动 ACP Agent、维持 20 分钟 Warm Session 热会话。 | 严禁让编码 Agent 自己执行看板流转命令或手动合并分支。 |
| **审查与验收** | 监听 `xissue` 的 `review` 状态：<br>1. 跑 `touches` 越界检查 + `verify_cmd` 实测（0 Token 门禁）<br>2. 拉起异构只读模型做交叉 Review（二轮仅审增量 Diff）<br>3. 失败则调 `xissue` 热会话打回（最多 2 次），通过则调 `xissue` 合入主分支并销毁 Worktree。 | 提供 Diff/Worktree 路径查询、热会话 `SendMessage`、`Submit(mode="merge_local")` 合入并强删 Worktree。 | 严禁同模型自写自审；严禁保留已 `done` 卡片的 Worktree。 |
| **归档清理** | 当 `Done` 卡片超过 20 张、停留超过 24 小时、或同一 `Spec` 下全部子卡均 `Done` 时，自动移入 `Archive` 并同步归档 `xissue` 对应记录。 | 执行底层 SQLite `archived_at` 标记与日志落盘。 | 严禁无限制堆积 `Done` 列。 |

---

## 2. 状态机与流转规则

```text
backlog ──(人工或 Agent 确认 ready)──> todo ──(调度器: 槽位<5 + 依赖满足 + touches无冲突)──> running
   ▲                                   │                                                     │
   └──────────(撤回)───────────────────┘                                                     ▼
                                                                                          review
                                                                                             │
                         ┌───(FAIL 且 retry < 2: 热会话增量打回)─────────────────────────────┤
                         │                                                                   ├──(FAIL 且 retry >= 2: 熔断)──> blocked
                         ▼                                                                   ▼
                      running                                                              done ──(超20张 / 满24h / Spec全完)──> archived
```

### 2.1 进入 `todo` 的硬门禁（Go 代码校验，0 Token）
卡片从 `backlog` 进入 `todo` 时，必须通过 `ValidateTodoGate(card)` 机械校验，任一不满足直接拒绝：
1. `project_path` 必须是本机已存在的 Git 仓库目录（或在 `xissue` 中已注册的 Project）。
2. `priority` 属于 `P0 | P1 | P2 | P3 | P4`。
3. `touches` 非空（至少声明 1 个允许修改的相对路径前缀或 glob，如 `["internal/auth/", "cmd/main.go"]`），且不得包含 `..` 或绝对路径；`size=small` 最多 2 条、`size=large` 最多 3 条，超出直接拒绝并提示拆卡。
4. `verify_cmd` 非空（至少 1 条可在 Worktree 内非交互式执行的验收命令，如 `go test ./internal/auth/...`）。
5. `goal`、`out_of_scope`、`acceptance` 均非空，且不包含 `<FILL_IN>` 或 `TODO` 占位符。

### 2.2 `todo` 看护与等待原因

每轮 `Scheduler.Tick` 在派发前调用看护逻辑，把“为什么这张卡还没跑”写入 `wait_reason` / `wait_since`：

1. **永久缺陷**：`depends_on` 引用了不存在的卡，或依赖成环 → 直接置 `blocked`，`last_error` 给出修正建议。
2. **暂时等待**：依赖未完成、上游 `blocked`、同项目 `touches` 冲突、槽位已满 → 保持 `todo`，记录原因；上游恢复后自动重新参与派发。
3. **停滞标记**：等待时长超过 `todoStallMinutes`（默认 15）时，TUI/`xkb list`/`xkb_board_status` 标记 `Stalled`，不静默死在 `todo`。
4. **自愈**：卡片被重新派发、重试、完成或重新提升时清空等待状态；残留的 `xissue_run_id`/`started_at` 会被清理。

---

## 3. 平价模型路由策略（`omp` + `devin swe-2-high`）

在 `~/.xkb/config.yaml` 中配置模型路由，遵循 **“好钢花在刀刃、写审必定异构、二轮只审增量”** 原则：

| 阶段 | 条件 | 默认 Agent / 模型 | 说明 |
| :--- | :--- | :--- | :--- |
| **Spec 拆解** | 交互式拆卡 | `devin` (`swe-2-high`) | 负责理解需求、划分 `touches` 边界与 `depends_on`。 |
| **编码 (`Running`)** | `size == "small"` 且 `priority in [P2, P3, P4]` | `omp` (`opencode`) | 80% 边界清晰的小卡用低成本模型快速完成。 |
| **编码 (`Running`)** | `size == "large"` 或 `priority in [P0, P1]` | `devin` (`swe-2-high`) | 核心状态机、跨模块契约、高优任务用强模型。 |
| **审查 (`Review`)** | 编码者为 `omp` | `devin` (`swe-2-high`) | 异构交叉审查，只读模式。 |
| **审查 (`Review`)** | 编码者为 `devin` | `omp` (`opencode`) | 异构交叉审查，只读模式。 |

### 3.1 免审白名单（节省 Reviewer Token）
当阶段 1（`touches` 边界检查 + `verify_cmd` 测试）全绿后，若满足以下**全部条件**，跳过阶段 2 的 AI Reviewer，直接合入并标记 `done`：
- 改动代码文件数 `<= 1` 且总增删行数 `<= 10`（或者仅修改了 `.md` 文档文件）；
- 卡片 `security_sensitive == false` 且 `priority` 不是 `P0`。

---

## 4. 数据存储与 Schema (`~/.xkb/xkb.db`)

采用单文件 SQLite（`modernc.org/sqlite` 纯 Go 无 CGO，开启 `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`），保证 `xkb` TUI、`xkb daemon`、`xkb mcp` 多进程并发读写安全。

### 4.1 `specs` 表（需求文档）
```sql
CREATE TABLE IF NOT EXISTS specs (
    id           TEXT PRIMARY KEY,               -- 如 spec-20261031-01
    project_path TEXT NOT NULL,                  -- 本地仓库绝对路径
    xissue_proj   INTEGER NOT NULL DEFAULT 0,     -- 关联的 xissue projectId
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,                  -- 完整 Spec Markdown
    status       TEXT NOT NULL DEFAULT 'active', -- active | done | archived
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 4.2 `cards` 表（原子任务卡）
```sql
CREATE TABLE IF NOT EXISTS cards (
    id                 TEXT PRIMARY KEY,               -- 如 xkb-001
    spec_id            TEXT NOT NULL DEFAULT '',       -- 关联 specs.id（可为空表示独立卡）
    project_path       TEXT NOT NULL,
    xissue_proj        INTEGER NOT NULL DEFAULT 0,
    xissue_issue_id    INTEGER NOT NULL DEFAULT 0,     -- 派发后关联的 xissue issueId
    xissue_run_id      INTEGER NOT NULL DEFAULT 0,     -- 当前活跃的 xissue runId
    title              TEXT NOT NULL,
    priority           TEXT NOT NULL DEFAULT 'P2',     -- P0 | P1 | P2 | P3 | P4
    size               TEXT NOT NULL DEFAULT 'small',  -- small | large
    security_sensitive INTEGER NOT NULL DEFAULT 0,     -- 0 | 1
    status             TEXT NOT NULL DEFAULT 'backlog',-- backlog | todo | running | review | done | blocked | archived
    depends_on_json    TEXT NOT NULL DEFAULT '[]',     -- 前置卡 ID 数组，如 ["xkb-001"]
    touches_json       TEXT NOT NULL DEFAULT '[]',     -- 允许修改的路径数组，如 ["internal/api/"]
    verify_cmd         TEXT NOT NULL DEFAULT '',       -- 验收执行命令
    goal               TEXT NOT NULL DEFAULT '',
    out_of_scope       TEXT NOT NULL DEFAULT '',       -- 禁区说明
    acceptance         TEXT NOT NULL DEFAULT '',       -- Markdown checklist
    exec_agent         TEXT NOT NULL DEFAULT '',       -- 实际执行的 agent
    exec_model         TEXT NOT NULL DEFAULT '',
    review_agent       TEXT NOT NULL DEFAULT '',       -- 实际审查的 agent
    retry_count        INTEGER NOT NULL DEFAULT 0,     -- 被打回次数（上限 2）
    last_reviewed_sha  TEXT NOT NULL DEFAULT '',      -- 上一轮审过的 commit SHA（用于二轮增量审查）
    last_error         TEXT NOT NULL DEFAULT '',      -- 阻塞或打回原因摘要
    wait_reason        TEXT NOT NULL DEFAULT '',      -- 为什么还在 todo（等待原因）
    wait_since         DATETIME,                       -- 等待开始时间（用于 Stalled 判定）
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at         DATETIME,
    finished_at        DATETIME,
    archived_at        DATETIME
);
```

### 4.3 `review_runs` 表（审查证据记录）
```sql
CREATE TABLE IF NOT EXISTS review_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    card_id      TEXT NOT NULL,
    round        INTEGER NOT NULL,
    stage        TEXT NOT NULL,     -- gate_cmd | ai_review
    reviewer     TEXT NOT NULL,
    base_sha     TEXT NOT NULL,
    head_sha     TEXT NOT NULL,
    verdict      TEXT NOT NULL,     -- PASS | FAIL
    findings_json TEXT NOT NULL,    -- 结构化问题列表
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

---

## 5. 核心算法规范

### 5.1 `Max=5` 冲突感知调度算法 (`Scheduler.Tick`)
每隔 2 秒（或收到 `xissue` WebSocket 事件时）执行一次：
1. **统计在途槽位**：查询 `status IN ('running', 'review')` 的卡片列表 `activeCards`。若 `len(activeCards) >= cfg.MaxConcurrent`（默认 5），直接返回。
2. **拉取候选卡**：查询 `status = 'todo'` 的所有卡片，按 `priority ASC (P0->P4), created_at ASC` 排序。
3. **过滤依赖与路径冲突**：遍历候选卡 `c`：
   - **依赖检查**：`c.DependsOn` 中的所有卡片必须处于 `done` 或 `archived` 状态；若有未完成依赖则跳过。
   - **同项目路径冲突检查**：遍历当前 `activeCards` 中同一 `project_path` 的在途卡 `a`，调用 `PathsOverlap(c.Touches, a.Touches)`：
     - 规则：规范化路径后，若任意路径存在前缀包含关系（如 `internal/` 与 `internal/auth/a.go`）或 glob 交集，则判定为冲突，跳过该候选卡 `c`。
   - **等待原因落库**：对每张未派发的 `todo` 卡写入 `wait_reason` / `wait_since`（依赖未完成、上游 blocked、路径冲突、槽位满、上次派发失败）。
   - **永久缺陷熔断**：依赖 ID 不存在或依赖成环时直接置 `blocked`，避免卡片永久静默；上游恢复后依赖卡自动回到可派发状态。
4. **派发执行**：
   - 选中无冲突卡片 `c`，通过路由器决定 `(execAgent, execModel)`。
   - 调用 `xissue` REST API：
     1. 确保 `project_path` 在 `xissue` 中有对应的 Project/Repo（无则自动调 `POST /api/projects` + `POST /api/projects/{id}/repos` 注册）。
     2. `POST /api/projects/{id}/issues` 创建对应任务（带精简 Prompt）。
     3. `POST /api/issues/{issueId}/runs` 启动 Run。
   - 将卡片状态更新为 `running`，记录 `xissue_issue_id`、`xissue_run_id`、`started_at`。

### 5.2 两阶段独立验收与自动合入算法 (`Verifier.ProcessReviewCard`)
当检测到 `xissue` 中 `run.Status == "completed"`（对应 Issue 进入 `review`）时触发：

1. **获取变更与工作区**：
   - 调用 `GET /api/runs/{runId}/changes?patch=1`（若 `xissue` 尚未升级该字段，则回退到本地 `~/.xissue/runs/run-{runId}/{repoName}` 执行 `git diff`）。
2. **阶段 1：0 Token 机械硬门禁**：
   - **禁区越界检查**：检查所有变更文件 `changedFiles` 是否全部匹配 `card.Touches`。若有越界文件 `f`，立即构造打回消息：`"越界修改了禁区外文件: <f>，请立即 git checkout 还原该文件，仅允许修改: <touches>"`。
   - **规模预算**：`small` 默认 2 文件 / 120 行，`large` 默认 4 文件 / 300 行；超限直接打回并提示拆卡，不再运行 `verify_cmd`，避免大卡长时间占用槽位。
   - **验收命令实测**：在 `worktreePath` 下通过 `sh -c <card.VerifyCmd>` 执行验收（超时默认 3 分钟）。若退出码 `!= 0`，截取末尾 80 行报错输出。
   - **阶段 1 打回处置**：若越界或命令失败：
     - 若 `card.RetryCount >= 2`：标记 `card.Status = 'blocked'`，写入 `last_error`，取消/关闭热会话，释放槽位。
     - 否则：`card.RetryCount++`，调用 `POST /api/runs/{runId}/message` 将具体报错发给 `xissue` 热会话立即重修，`card.Status` 回到 `running`（**0 Reviewer Token 消耗**）。
3. **阶段 2：异构只读 AI 审查**：
   - 若命中 3.1 免审白名单，直接跳至第 4 步合入。
   - 选择与 `exec_agent` 异构的 `review_agent`（`omp` ⇄ `devin`）。
   - **增量 Diff 计算**：
     - 若 `card.LastReviewedSHA == ""`（首轮审查）：取 `mergeBase..HEAD` 的完整 Diff。
     - 若 `card.LastReviewedSHA != ""`（二轮增量复审）：仅取 `card.LastReviewedSHA..HEAD` 的增量 Diff + 上一轮未关闭的 findings。
   - 以只读方式调用 Reviewer CLI（如 `opencode run` 带 `OPENCODE_PERMISSION` 只读策略，或 `devin` 只读 Prompt），要求只输出 JSON：
     ```json
     {
       "verdict": "PASS",
       "findings": [
         {"severity": "blocking|high|medium|low", "mechanical": false, "file": "a.go", "line": 42, "issue": "...", "fix": "..."}
       ]
     }
     ```
   - 仅当存在 `blocking | high | medium` 级别的 finding 时判定为 `FAIL`（若全部 finding 均为 `mechanical: true`，如注释/死代码，打回修复后下一轮仅跑阶段 1 即放行，不再调 AI Reviewer）。
4. **合入与清理（PASS 路径）**：
   - 调用 `xissue` 的 `POST /api/runs/{runId}/submit`：
     ```json
     {
       "mode": "merge_local",
       "commitMessage": "feat(xkb): <card.Title> (<card.ID>)"
     }
     ```
   - 若 `Submit` 返回合并冲突错误：若 `RetryCount < 2`，将冲突信息通过 `SendMessage` 发回热会话解决；否则置为 `blocked`。
   - 合入成功后（`xissue` 已自动强删 Worktree）：更新 `card.Status = 'done'`、`finished_at = now`。
   - 立即触发 `Archiver.CheckAndArchive()` 与 `Scheduler.Tick()`（自动拉起下游依赖卡）。

---

## 6. CLI、TUI 与 MCP 接口设计

### 6.1 CLI 命令契约
```bash
xkb                                  # 启动交互式终端 TUI 看板（内置后台调度协程）
xkb daemon                           # 纯后台常驻运行调度器 + 验收器 + 归档器
xkb spec new --project <dir> --title "..." --file spec.md
xkb add --project <dir> --priority P1 --touches "internal/a/" --verify "go test ./..." --title "..." --file card.md
xkb ready <card-id>...               # 校验硬门禁并将卡片从 backlog 推入 todo
xkb list [--status backlog|todo|running|review|done|blocked|archived] [--json]
xkb show <card-id> [--json]
xkb retry <card-id>                  # 将 blocked 卡片重置 retry_count=0 并放回 todo
xkb archive [--all-done]             # 立即执行归档检查或清空当前 done 列
xkb mcp [--install]                  # 启动 stdio MCP Server 或一键注册到本机 Agent
```

### 6.2 TUI 界面与快捷键（基于 `charmbracelet/bubbletea` + `lipgloss`）
- **五列主视图**：`Backlog` | `Todo (P0~P4)` | `Running (n/5)` | `Review` | `Done`
- **底部状态栏**：显示当前槽位占用 `Slots: 3/5`、`xissue` 连接状态、各列卡片计数、最近一条事件日志。
- **快捷键**：
  - `h/l` 或 `←/→`：切换列；`j/k` 或 `↑/↓`：选择卡片
  - `Enter` / `Space`：弹窗查看卡片详情（契约、`touches`、审查结论、报错日志）
  - `t`：将选中 `Backlog` 卡片校验并推入 `Todo`（或从 `Todo` 退回 `Backlog`）
  - `p`：循环调整卡片优先级（`P0 -> P1 -> P2 -> P3 -> P4`）
  - `r`：重试 `Blocked` 卡片
  - `a`：一键将当前 `Done` 列归档进 `Archive`
  - `q`：退出

### 6.3 `xkb mcp` 工具清单（纯 `stdio` 直连 `~/.xkb/xkb.db`，零端口占用）
供日常对话 Agent（`omp` / `devin` / `claude`）在讨论完需求后直接落盘：
1. `xkb_create_spec`：保存需求 Spec 文档，返回 `spec_id`。
2. `xkb_create_cards`：批量创建拆解后的子任务卡（支持设置 `priority`、`depends_on`、`touches`、`verify_cmd`、`goal`、`out_of_scope`、`acceptance`、`auto_ready`）。
3. `xkb_promote_todo`：将指定 `backlog` 卡片校验并提升至 `todo` 队列触发自动调度。
4. `xkb_board_status`：查询当前看板各列任务、阻塞原因及 Spec 整体完成度。
