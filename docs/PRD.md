# Session Manager 产品需求文档

> 状态：Draft · 版本：0.1 · 日期：2026-07-30
> 产品代号：`sessionmgr`

## 1. 产品摘要

Session Manager 是一个本地优先、跨机器、跨 Agent 平台的 AI 编程工作留档与迁移工具。

它保存的不是一段孤立的聊天记录，也不是一个无法独立工作的 worktree，而是一次完整的 Agent 工作运行（Run）：

```text
Run = Workspace checkpoints + Agent sessions + Runtime context + Lineage
```

用户可以把一次运行归档为可验证、可加密、可迁移的 Capsule，在另一台机器上恢复代码现场，在相同 Agent 平台中尝试原生继续，或生成适用于另一 Agent 平台的 handoff。

## 2. 背景与问题

AI 编程 Agent 的 session 通常只在本机可见，并且与创建时的绝对工作目录、Git 状态、平台配置和本地附件隐式绑定。只复制 session 文件会丢失工作现场；只复制 worktree 又会丢失决策过程，而且 linked worktree 本身还依赖主仓库的 Git object database。

现有工作流存在以下问题：

1. 用户更换电脑或在多台电脑之间工作时，难以迁移本地 session。
2. Codex、Claude Code、Cursor 等平台使用不同的 session 格式，缺少统一归档层。
3. session 中的结论与实际代码状态可能不一致，恢复后很难确认 Agent 当时看到了什么。
4. 本地 commit、staged/unstaged 修改和 untracked 文件可能尚未推送，单纯 clone 仓库无法恢复现场。
5. session 可能包含源码、终端输出、凭据片段或私人路径，直接同步有明显安全风险。
6. 多设备同时写入 session 数据库或 JSONL 文件，可能造成覆盖、分叉或损坏。

## 3. 产品愿景

让一次 AI 编程工作像 Git commit 一样可定位，像 Git bundle 一样可搬运，像运行日志一样可审计，并能在任意支持的平台上继续。

一句话定位：

> Archive, move, and continue an AI coding run anywhere.

## 4. 目标

### 4.1 产品目标

- G1：无损保存平台原始 session，以便审计和未来重新解析。
- G2：把 session 与对应的 Git 工作现场绑定为一个可迁移单元。
- G3：在相同平台上提供原生恢复能力；无法原生恢复时提供明确降级。
- G4：在不同 Agent 平台之间生成可靠、可验证的 handoff。
- G5：支持用户自有存储和端到端加密，不要求托管云服务。
- G6：保留运行、checkpoint、fork 和 handoff 之间的 lineage。
- G7：默认不迁移 Agent 登录凭据、环境变量或其他机器身份信息。

### 4.2 MVP 成功标准

- 用户能在 5 分钟内把一个 Codex Run 从机器 A 搬到机器 B。
- 目标机器恢复后的 `HEAD`、staged diff、unstaged diff 和已选择的 untracked 文件与源机器一致。
- Capsule 能通过 checksum 完整性验证。
- 同一平台恢复失败时，用户仍能获得可用的 handoff 文档和工作现场。
- 默认 Capsule 中不包含 Agent 登录文件、Git credential helper 数据或完整环境变量。
- 两端存在同名或同 ID 数据时不静默覆盖。

## 5. 非目标

以下内容不属于 MVP：

- 对任意 Agent 平台进行确定性 replay。
- 迁移模型隐藏状态、服务端上下文或平台私有执行状态。
- 在两个设备上同时编辑同一个原生 session。
- 自动合并已经分叉的 session event log。
- 保存完整虚拟机、容器镜像或所有系统依赖。
- 云端多人实时协作。
- 替代 Git hosting、代码备份或密钥管理器。
- 迁移 Agent、Git hosting 或模型提供商的认证凭据。
- 在跨平台 handoff 中承诺“原生 resume”语义。

## 6. 目标用户

### 6.1 多设备独立开发者

在台式机、笔记本或远程开发机上轮换工作，希望保留 Agent session 和尚未推送的代码现场。

### 6.2 Agent 重度用户

同时使用 Codex、Claude Code、Cursor 等工具，希望统一查找历史运行，并在不同 Agent 之间继续。

### 6.3 需要审计与复盘的开发者

希望回答“Agent 做了什么、基于哪个代码状态、执行过哪些验证、为什么作出这个决定”。

团队共享、权限控制和组织级保留策略属于后续阶段。

## 7. 核心概念

| 概念 | 定义 |
| --- | --- |
| Run | 用户可见的顶层工作单元，包含一个或多个 workspace、session 和 checkpoint |
| Workspace | Agent 操作过的一个 Git 工作目录；MVP 中一个 Run 仅支持一个主 workspace |
| Checkpoint | workspace 在某一时刻的可恢复状态，与 session event 位置相关联 |
| Agent Session | 某个平台生成的原始会话与规范化事件 |
| Capsule | 一个可验证、可加密、可传输的 Run 快照 |
| Adapter | 负责发现、读取、规范化和可选恢复某个平台 session 的插件 |
| Store | Capsule 的本地或远程存储后端 |
| Handoff | 为另一 Agent 平台生成的、以继续工作为目标的上下文包 |
| Lineage | Run 的父子、fork、restore 和 handoff 关系 |

`(worktree, session)` 是最常见的用户心智模型，但底层模型必须允许一个 Run 引用多个 sessions 和 workspaces，避免被一对一关系限制。

## 8. 产品原则

1. **原始数据优先**：始终保留 raw session，规范化格式可以重新生成。
2. **不可变归档**：已提交到 Store 的 Run 不原地修改；新 capture 产生新 revision。
3. **恢复不覆盖**：默认恢复到新 worktree，遇到文件冲突立即停止。
4. **能力诚实**：清楚区分 Archive、Native Restore 和 Handoff。
5. **本地优先**：浏览、验证和生成 handoff 不依赖托管服务。
6. **默认最小披露**：不采集凭据，不默认采集 ignored 文件，不默认上传遥测。
7. **可验证**：所有对象均有内容摘要，恢复结果可以与 capture 状态比对。

## 9. 核心用户流程

### 9.1 Capture

1. 用户在一个 Git workspace 中选择 Agent session。
2. Session Manager 识别仓库、worktree、branch、HEAD 和 upstream。
3. 工具采集尚未推送的 commits、staged/unstaged 修改和用户选择的 untracked 文件。
4. Agent adapter 保存 raw session，并生成规范化事件。
5. 工具运行敏感信息检查，展示 capture 摘要。
6. 用户确认后生成不可变 Capsule。

### 9.2 Inspect

用户可以按仓库、Agent、机器、日期和 lineage 浏览 Run，并查看：

- Run 标题和目标；
- source machine 与原始路径提示；
- branch、commit、dirty 状态；
- session 数量和平台；
- 变更文件；
- 执行过的测试或验证；
- 安全扫描结果；
- Capsule 大小和完整性状态。

### 9.3 Restore

1. 用户在目标机器选择 Run。
2. 工具定位或 clone 对应仓库。
3. 默认创建一个新的 worktree。
4. 导入 Capsule 中的本地 commits。
5. 依次恢复 staged、unstaged 和 untracked 状态。
6. Agent adapter 尝试导入原生 session。
7. 如果平台不支持原生导入，则生成 handoff，并提供启动目标 Agent 的命令。

### 9.4 Handoff

用户选择目标 Agent 平台。工具生成一个事实可追溯的上下文包，至少包含：

- 原始目标；
- 已完成工作；
- 关键决定与依据；
- 当前 Git 状态；
- 修改文件及摘要；
- 已执行验证及结果；
- 未解决问题；
- 建议下一步；
- 相关 checkpoint 和原始事件引用。

### 9.5 Push / Pull

- Push 只上传不可变对象，最后原子更新 Run ref。
- Pull 合并 Run 集合，不修改已有 Run。
- 同一 lineage 在不同机器产生新 revision 时保留两个分支。
- 远程存储默认要求加密。

## 10. 功能需求

### 10.1 Run 与目录

- **FR-RUN-001**：系统必须为每个 Run 生成全局唯一、按时间可排序的 ID。
- **FR-RUN-002**：系统必须记录 Run 的 parent、fork、restore 和 handoff lineage。
- **FR-RUN-003**：已保存的 Run 必须不可变；后续 capture 创建新 Run 或 revision。
- **FR-RUN-004**：系统必须允许用户重命名和添加 tags，且不改变 Capsule 内容。
- **FR-RUN-005**：目录必须支持按 repo、Agent、日期、机器和状态过滤。

### 10.2 Workspace capture

- **FR-WS-001**：必须识别 Git common directory 与当前 worktree root。
- **FR-WS-002**：必须记录规范化 remote、branch、HEAD、upstream 和 Git 版本。
- **FR-WS-003**：必须保存恢复所需且远端不可获得的本地 commits。
- **FR-WS-004**：必须分别保存 staged 和 unstaged binary-safe diff。
- **FR-WS-005**：默认保存非 ignored 的 untracked 文件，但 capture 前必须展示数量和大小。
- **FR-WS-006**：ignored 文件默认排除，用户只能通过显式参数加入。
- **FR-WS-007**：必须检测 submodule、Git LFS 和 sparse checkout，并对不完整 capture 给出警告。
- **FR-WS-008**：不得把 `.git`、Git credentials 或 credential helper 数据作为 workspace 文件归档。
- **FR-WS-009**：必须记录 source path hint，但远程 Capsule 中不得暴露未脱敏的 home directory。

### 10.3 Agent session

- **FR-AGENT-001**：adapter 必须声明 `archive`、`normalize`、`native_restore` 和 `handoff` 能力。
- **FR-AGENT-002**：必须无损保存 raw session 和 adapter 版本。
- **FR-AGENT-003**：必须生成平台无关的 event stream，至少表示 user、assistant、tool、result 和 checkpoint。
- **FR-AGENT-004**：不得保存平台登录凭据或认证数据库。
- **FR-AGENT-005**：当 native restore 不可用或失败时，必须自动提供 handoff 降级路径。
- **FR-AGENT-006**：MVP 必须提供 Codex adapter。

### 10.4 Capsule

- **FR-CAP-001**：Capsule 必须包含 versioned manifest。
- **FR-CAP-002**：所有 payload object 必须有 SHA-256 checksum 和字节长度。
- **FR-CAP-003**：Capsule 必须能离线执行结构与 checksum 验证。
- **FR-CAP-004**：解包必须防止绝对路径、`..`、危险 symlink 和覆盖攻击。
- **FR-CAP-005**：未知的可选字段必须可忽略；未知的必需能力必须拒绝恢复。
- **FR-CAP-006**：远程传输的 Capsule 默认必须端到端加密。

### 10.5 Restore

- **FR-RESTORE-001**：默认恢复到新建 worktree，不直接修改现有 dirty workspace。
- **FR-RESTORE-002**：必须在写入前验证 Capsule。
- **FR-RESTORE-003**：必须先恢复 commits，再恢复 staged、unstaged 和 untracked 状态。
- **FR-RESTORE-004**：目标文件存在且内容不同时必须停止，不得静默覆盖。
- **FR-RESTORE-005**：恢复结束后必须重新计算 workspace digest 并报告差异。
- **FR-RESTORE-006**：原始绝对路径不存在时，必须允许用户显式映射到目标 workspace。
- **FR-RESTORE-007**：必须生成机器可读的 restore report。

### 10.6 Handoff

- **FR-HANDOFF-001**：handoff 必须区分事实、推断和建议。
- **FR-HANDOFF-002**：每个关键事实应能引用 session event、Git diff 或 checkpoint。
- **FR-HANDOFF-003**：handoff 不得声称未执行的测试已经通过。
- **FR-HANDOFF-004**：MVP 至少输出通用 Markdown handoff。
- **FR-HANDOFF-005**：平台 adapter 可以额外输出目标平台专用启动文件或 prompt。

### 10.7 Store 与同步

- **FR-SYNC-001**：MVP 必须支持本地目录 Store。
- **FR-SYNC-002**：MVP 必须支持通过 SSH 使用用户自有远程存储。
- **FR-SYNC-003**：对象必须先上传，Run ref 必须最后原子更新。
- **FR-SYNC-004**：Pull 必须采用集合并集，不删除本地独有 Run。
- **FR-SYNC-005**：删除必须使用显式 tombstone，并在确认后执行垃圾回收。
- **FR-SYNC-006**：中断的同步必须可安全重试。

### 10.8 安全与隐私

- **FR-SEC-001**：capture 必须运行 secret scan，并把发现分为 block、warn 和 info。
- **FR-SEC-002**：命中高置信度私钥或访问 token 时，远程 push 默认阻止。
- **FR-SEC-003**：用户必须能在 capture 前查看文件清单和总大小。
- **FR-SEC-004**：远程 Store 不得获得明文加密密钥。
- **FR-SEC-005**：MVP 默认禁用遥测。
- **FR-SEC-006**：日志不得记录 session 正文、密钥、完整 remote credential 或文件内容。

## 11. 非功能需求

- **NFR-001 可靠性**：capture 与 restore 必须通过临时目录构建，并以原子 rename 发布结果。
- **NFR-002 性能**：对 1 GB 以下 workspace payload，catalog 操作不应读取全部对象内容。
- **NFR-003 可移植性**：MVP 支持 macOS 与 Linux；Windows/WSL 后续支持。
- **NFR-004 可扩展性**：新增 Agent 或 Store 不应修改核心 domain model。
- **NFR-005 可审计性**：所有写操作必须生成结构化 operation report。
- **NFR-006 可恢复性**：进程退出或网络中断不得留下可见的半成品 Run。
- **NFR-007 可复现性**：相同输入产生的 payload object digest 必须稳定。
- **NFR-008 可访问性**：CLI 的关键信息必须有纯文本和 JSON 输出。

## 12. MVP 范围

### 12.1 包含

- CLI 产品；
- macOS、Linux；
- 单 Git 仓库、单主 workspace；
- Codex raw session capture 与基础 normalization；
- 当前 checkpoint capture；
- 本地 commits、staged/unstaged diff、untracked 文件；
- 本地 Store 与 SSH Store；
- 内容校验与加密传输；
- 默认恢复到新 worktree；
- Codex native restore 的实验性支持；
- 通用 Markdown handoff；
- 本地 catalog、筛选和 lineage。

### 12.2 不包含

- GUI；
- 多仓库 Run；
- session 运行中的自动逐事件 checkpoint；
- Claude Code、Cursor 等平台的无损 adapter；
- 团队账户与权限；
- 自动云端总结；
- ignored 文件的自动推断；
- 实时同步。

## 13. MVP 验收场景

### AC-001：同平台跨机器恢复

给定机器 A 上一个包含本地 commit、staged、unstaged 和 untracked 状态的 Codex Run，当用户 capture、push，并在机器 B pull、restore 后：

- workspace digest 一致；
- Codex session 可以原生打开，或明确降级为 handoff；
- 原机器路径不要求在机器 B 存在；
- 不迁移 Codex 登录状态。

### AC-002：跨平台 handoff

给定一个 Codex Run，当用户生成通用 handoff 后：

- 文档包含目标、完成项、决定、Git 状态、验证和待办；
- 所有测试结论与 raw session 一致；
- 用户可在恢复后的 worktree 中把 handoff 交给另一 Agent。

### AC-003：冲突保护

给定目标 worktree 中已经存在不同内容的文件，当 restore 尝试写入该路径时：

- restore 停止；
- 原文件不改变；
- report 列出冲突路径和建议操作。

### AC-004：安全阻断

给定 untracked 文件中包含高置信度私钥，当用户 push 到远程 Store 时：

- push 被阻止；
- 输出命中文件和规则，不打印秘密正文；
- 用户可以排除文件后重新 capture。

### AC-005：同步中断

给定上传过程中网络中断，当用户重新执行 push 时：

- 已上传对象被复用；
- 未完成 Run 不出现在远程目录；
- 最终 ref 只在所有对象可用后发布。

## 14. 成功指标

MVP 内测阶段关注：

- Capture 成功率；
- Restore workspace digest 一致率；
- 原生恢复成功率与降级率；
- 平均 capture、push、pull、restore 时间；
- Capsule 去重率；
- secret scan 阻断次数；
- 用户从选择 Run 到在目标机器继续工作的时间；
- 不可恢复或静默数据丢失事件数，目标为零。

默认不上传遥测。指标通过用户主动导出的匿名 operation reports 或测试环境收集。

## 15. 风险与缓解

| 风险 | 缓解方式 |
| --- | --- |
| Agent 私有格式变化 | 保留 raw 数据；adapter versioning；fixture contract tests |
| 原生 session 无公开导入协议 | 将 native restore 标记为 capability；始终提供 handoff |
| worktree 依赖主仓库 | 以 Git object、commit、patch 和文件对象恢复，不复制 `.git` 指针 |
| session 泄露秘密 | 默认排除、secret scan、远程强制加密、清单确认 |
| 大型 untracked 文件导致 Capsule 膨胀 | 大小阈值、内容寻址去重、显式确认 |
| 两设备同时写入 | immutable Run、lineage fork、ref 原子更新，不合并 event log |
| Git LFS/submodule 不完整 | capture 诊断、明确 capability warning、恢复后校验 |

## 16. 里程碑

### M0：格式与本地原型

- Run/Capsule schema；
- Git workspace capture/restore；
- 本地 Store；
- checksum 与 operation reports。

### M1：Codex 可用闭环

- Codex discovery、raw capture、normalization；
- 本地 catalog；
- Codex 实验性 native restore；
- Markdown handoff。

### M2：跨机器

- SSH Store；
- 加密；
- push/pull；
- 冲突与断点重试测试。

### M3：跨平台

- Adapter SDK；
- 第二个 Agent adapter；
- platform-specific handoff；
- lineage 浏览。

## 17. 待验证事项

以下事项需要在实现前通过 spike 或 fixture 验证：

1. Codex 各版本对导入 JSONL session 的重建索引行为。
2. Codex App 与 CLI 对 session title、archive 状态和 cwd override 的一致性。
3. Git bundle 在 detached HEAD、无 upstream、shallow clone 和 partial clone 下的行为。
4. staged/unstaged binary patch 的跨平台恢复一致性。
5. symlink、文件权限和大小写不敏感文件系统之间的兼容策略。
6. SSH Store 的最小原子操作与锁语义。
