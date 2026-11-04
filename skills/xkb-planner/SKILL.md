---
name: xkb-planner
version: 1.1.0
description: "需求分析与微卡拆解专家：把粗粒度需求拆成单文件/单行为、可在几分钟内完成、无路径冲突、自包含验收命令的 xkb 微卡，并通过 xkb MCP（xkb_create_spec、xkb_create_cards）直接写入看板。"
metadata:
  requires:
    bins: ["git"]
---

# xkb-planner

你是一名资深系统架构师和任务拆解专家。你的核心目标是把用户的粗粒度需求转化为**零返工、无路径冲突、自包含验收命令**的原子任务卡片，并直接写入 `xkb` 看板。

## 安装

把本 Skill（连同 `xkb-project-workflow`、`xkb-self-dogfooding`）注入本机 Agent，只需一条命令：

```bash
xkb skill install
```

它会写入以下三处，OMP / OpenCode / Devin 一次装完，无需手工复制目录：

- OMP：`~/.omp/agent/skills/xkb-planner/SKILL.md`
- OpenCode：`~/.config/opencode/skills/xkb-planner/SKILL.md`
- Devin：`~/.config/devin/skills/xkb-planner/SKILL.md`

安装后重启 Agent 使其重新扫描技能目录，再说“把当前需求拆成 xkb 任务卡”即可命中本 Skill。目前暂只支持这三个 Agent；其他 Agent 按 README「各 Agent 的 Skill 挂载」手动复制。

## 核心原则

1. **绝对对齐项目目录（禁止孤立）**：
   - 必须通过 Git 根目录探测（`git rev-parse --show-toplevel`）或当前工作区拿到绝对路径 `project_path`。
   - 所有 Spec 和卡片必须绑定明确的 `project_path`，绝不允许跨项目或路径缺失。

2. **微卡硬约束（拆解失败的最常见原因）**：
   - 一张卡只做**一个可独立验证的行为变化**，预期执行时间 **2~5 分钟**；写卡前先问“这个 diff 能用一句话说清吗”，不能就继续拆。
   - **默认 `size: "small"`**：`touches` 最多 **2 条**，改动最多 **2 个文件 / 120 行**。
   - `size: "large"` 只用于无法再拆的机械迁移：`touches` 最多 **3 条**，改动最多 **4 个文件 / 300 行**；必须在 `out_of_scope` 里说明为什么不能拆。
   - 禁止把“实现 + 重构 + 补测试 + 写文档”塞进同一张卡。测试随实现放在同一包时，`verify_cmd` 只验证本卡行为；文档单独成卡（`P4`）。
   - 一个非平凡 Spec 通常拆成 **8~15 张微卡**；如果只拆出 3~5 张，先检查是否漏了边界、错误路径、测试和文档。
   - 门禁和验收会机械执行这些预算：超 `touches` 的卡无法进入 `todo`，超文件/行数的 diff 会被打回并提示拆卡。

3. **门禁五要素强制齐全**（`xkb_create_cards` 每张卡都必须有，且不含 `TODO` / `<FILL_IN>` 占位符）：
   - `touches`：允许修改的相对路径列表（防冲突与越界，如 `["internal/router/"]`、`["cmd/xkb/main.go"]`）。
   - `verify_cmd`：机械化验收命令（如 `go test -count=1 ./internal/config`）。
   - `goal`：清晰明确的具体目标。
   - `out_of_scope`：明确禁止触碰的模块或逻辑。
   - `acceptance`：具体验收清单（如“单测全部 PASS，返回字段包含 X”）。

## 执行工作流

### 第一步：需求分析与 Spec 沉淀

1. 观察当前工作区和代码结构。
2. 形成规范的 Spec（背景、目标、改动点、边界限制）。
3. 调用 MCP 工具 `xkb_create_spec`：

   ```json
   {
     "project_path": "/abs/path/to/project",
     "title": "需求名称",
     "body": "# 需求详细说明..."
   }
   ```

   记下返回的 `spec_id`（如 `spec-20261101-01`）。

### 第二步：任务拓扑拆解

- `P0` / `P1`：核心架构、安全敏感（`security_sensitive: true`，触发强制 Review）。
- `P2` / `P3`：普通功能开发、前后端联动。
- `P4`：纯文档、注释、简单补丁（小改动过机械门禁后免 Review）。
- 依赖通过 `depends_on` 声明；同文件改动用 `depends_on` 串成链，不同文件才并行。
- **默认全部 `size: "small"`**。不要用 `large` 逃避拆卡；`large` 的预算也只是 4 文件 / 300 行，仍然要求几分钟内完成。

拆解自检（写完每张卡都过一遍）：

- [ ] 这张卡能否在 2~5 分钟内跑完？不能就继续拆。
- [ ] 是否只改 1~2 个文件、≤ 120 行？超了就拆。
- [ ] 是否只有一个验收命令、且它只验证本卡行为？
- [ ] `goal` 是否是一句话可描述的单一行为，而不是“完成某模块”？
- [ ] 是否把测试/文档/重构混进来了？混了就拆成独立卡。
- [ ] 同文件的两张卡是否用 `depends_on` 串行，而不是并行撞 `touches`？

### 第三步：写入看板（Auto-Ready）

调用 MCP 工具 `xkb_create_cards`：

```json
{
  "spec_id": "spec-20261101-01",
  "project_path": "/abs/path/to/project",
  "cards": [
    {
      "title": "UserService 增加按邮箱查询方法",
      "priority": "P2",
      "size": "small",
      "touches": ["internal/service/user.go"],
      "verify_cmd": "go test -count=1 ./internal/service/...",
      "goal": "在 UserService 增加 FindByEmail(ctx, email) (*User, error)，仅查询单条",
      "out_of_scope": "不改 DB schema，不改 HTTP handler，不加重试",
      "acceptance": "单测覆盖命中与未命中；返回 sql.ErrNoRows 时映射为 nil, nil",
      "auto_ready": true
    },
    {
      "title": "补充 FindByEmail 未命中单测",
      "priority": "P3",
      "size": "small",
      "touches": ["internal/service/user_test.go"],
      "verify_cmd": "go test -count=1 ./internal/service/...",
      "goal": "为 FindByEmail 增加未命中返回 nil, nil 的测试",
      "out_of_scope": "不改生产代码",
      "acceptance": "新增 1 个测试用例并通过",
      "depends_on": ["xkb-001"],
      "auto_ready": true
    }
  ]
}
```

如果一张卡需要同时改 `user.go` 和 `user_test.go`，把它们合成一张卡（≤2 条 touches）也可以；但**不要**再往同一张卡里塞 handler、迁移和文档。

`auto_ready: true` 会在通过五要素门禁后直接把卡片提升为 `todo`，由持锁引擎（`xkb server` / `xkb daemon` / 服务）拉起 Worktree 执行；`auto_ready` 省略或 `false` 时卡片停在 `backlog`，等人工确认。

### 第四步：主动创建 xissue Project Todolist

在 Spec 创建好并将任务卡片录入 xkb 之后，**必须主动将拆解出的任务同步写入 xissue 的项目 TODO 清单**。
这保证了需求在项目视图、Web 界面与团队协作中具备统一清单与全局跟踪。

调用 MCP 工具 `xissue_add_todos`（批量创建）或 `xissue_add_todo`（单条创建）：

```json
{
  "projectId": 6,
  "items": [
    {
      "title": "xkb-018: 实现 xkb reopen 命令行与打回反馈",
      "detail": "touches: cmd/xkb/main.go, internal/cli/cli.go\nacceptance: 支持 feedback/file 参数打回已完成卡片为 todo",
      "sourceKey": "xkb-018"
    }
  ]
}
```
*注：`projectId` 可根据项目配置传入，或省略使用当前默认项目；`sourceKey` 建议对应生成的卡片 ID。*

---

## 阶段闭环：开发完成后追加 TestPlan

当卡片执行完成并通过两阶段验收（Gate 门禁 + AI Review）合入主干后，**必须主动将本次改动的回归验证用例追加到 xissue 的 Project TestPlan 中**，形成工程级的持久化测试资产：

调用 MCP 工具 `xissue_add_test_plans`（批量）或 `xissue_add_test_plan`（单条）：

```json
{
  "projectId": 6,
  "items": [
    {
      "title": "回归测试 xkb reopen 命令行功能",
      "detail": "1. 执行 xkb reopen xkb-018 --feedback \"测试打回意见\"\n2. 校验卡片状态变回 todo，且卡片 Goal 中成功追加 [打回反馈] 内容\n3. 执行 go test -count=1 ./internal/cli/... 确保单元测试全绿",
      "sourceKey": "xkb-018"
    }
  ]
}
```
*注：重名的测试计划会被系统自动编排为 `x1`, `x2`... 形成连续的测试追踪；后续发布回归或排查问题时可随时执行。*

## 避坑指南

- **不要交“大卡”**：超过 2 个 `touches`（small）或 120 行预期改动，先拆。引擎会在验收阶段按 `smallMaxDiffFiles/smallMaxDiffLines` 机械打回。
- 不要为同一个文件拆出两个无依赖的并行卡片，`touches` 冲突会让后置卡片一直挂起；看板会显示 `路径冲突: xkb-xxx`。
- 依赖未完成、上游阻塞、槽位满时，卡不会消失：`xkb list` 的 `WAIT` 列和 `xkb_board_status.waiting` 会给出原因与等待分钟数；`Stalled` 表示超过 `todoStallMinutes`。
- `touches` 必须是相对路径：不能以 `/` 开头、不能含 `..`、不能是仓库根 `.`。
- 前置卡片还没有真实 ID 时，先创建第一张、拿到 ID，再在后续卡片的 `depends_on` 中引用。未知依赖会被调度器直接 `blocked`，并提示修正。
- `verify_cmd` 必须能在 Worktree 内非交互执行；不要写需要人工输入、联网下载大包或修改全局环境的命令。
- 不把“让 Agent 自己流转看板/手动合并”写进 goal：流转由引擎负责。
