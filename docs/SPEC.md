# Session Manager 技术规格

> 状态：Draft · 版本：0.1 · 日期：2026-07-30
> 关联文档：[PRD](./PRD.md)

## 1. 规格范围

本规格定义 Session Manager MVP 的架构、领域模型、Capsule 格式、Git capture/restore 算法、Codex adapter、CLI、Store 协议、安全边界和验收测试。

实现基线采用 Go，核心原因是跨平台单二进制、流式归档、并发上传和部署成本低。Git 操作通过系统 `git` CLI 完成，不在 MVP 中重新实现 Git object protocol。

## 2. 系统边界

```text
┌──────────────┐
│     CLI      │
└──────┬───────┘
       │
┌──────▼─────────────────────────────────────┐
│                Core Service                │
│ Run orchestration · policy · operation log │
└───┬──────────┬────────────┬───────────┬────┘
    │          │            │           │
┌───▼───┐  ┌───▼────┐  ┌────▼────┐  ┌───▼─────┐
│ Agent │  │ Git/VCS│  │ Capsule │  │  Store  │
│Adapter│  │ Engine │  │  Codec  │  │ Backend │
└───────┘  └────────┘  └─────────┘  └─────────┘
```

### 2.1 组件

| 组件 | 职责 |
| --- | --- |
| CLI | 参数解析、交互确认、人类可读与 JSON 输出 |
| Core Service | Run 生命周期、capability negotiation、策略执行 |
| Agent Adapter | session discovery、raw capture、normalization、native restore |
| Git Engine | repo 识别、commit/diff/untracked capture、worktree restore |
| Capsule Codec | manifest、对象编码、checksum、归档、加密 |
| Store Backend | 本地和 SSH 对象存储、refs、原子发布 |
| Catalog | 本机 Run 索引、tags、路径映射、同步状态 |
| Secret Scanner | 文件名、内容规则和高熵检查 |
| Handoff Renderer | 从 normalized events 和 Git facts 生成 handoff |

## 3. 目录与配置

### 3.1 Session Manager Home

环境变量 `SESSIONMGR_HOME` 可以覆盖默认位置。MVP 默认：

```text
~/.sessionmgr/
├── config.toml
├── catalog.sqlite
├── objects/
├── runs/
├── refs/
├── keys/
├── cache/
├── tmp/
└── operation-reports/
```

权限要求：

- home、keys、tmp：`0700`
- key material：`0600`
- 普通 object：`0600`

### 3.2 配置示例

```toml
schema_version = 1
default_store = "local"
telemetry = false

[capture]
include_untracked = true
include_ignored = false
max_file_bytes = 268435456
max_total_bytes = 1073741824

[security]
block_private_keys = true
block_high_confidence_tokens = true

[[stores]]
name = "local"
type = "file"
url = "~/.sessionmgr"

[[stores]]
name = "personal-ssh"
type = "ssh"
url = "ssh://devbox.example.com/~/sessionmgr-store"
age_recipients = ["age1example"]
```

## 4. 领域模型

### 4.1 Run

Run 是不可变的顶层对象。重新 capture 会创建新 Run，并通过 `parent_run_id` 形成 lineage。

```go
type Run struct {
    SchemaVersion int
    ID            string // UUIDv7
    Title         string
    CreatedAt     time.Time
    CreatedBy     MachineIdentity
    Runtime       RuntimeContext

    ParentRunID   string
    Relation      string // capture, revision, fork, restore, handoff

    Workspaces    []WorkspaceSnapshot
    Sessions      []AgentSession
    Checkpoints   []Checkpoint
    Objects       []ObjectDescriptor

    Security      SecurityReportSummary
    Capabilities  []string
}
```

`RuntimeContext` 只记录恢复和诊断所需的非秘密信息：

```go
type RuntimeContext struct {
    OS            string
    Arch          string
    ShellName     string
    GitVersion    string
    AgentVersions map[string]string
}
```

它不得保存环境变量值、hostname、用户名、IP 地址或 credential helper 输出。`MachineIdentity` 是本地随机生成的稳定 ID，不从硬件序列号或 hostname 推导。

MVP 约束：

- `len(Workspaces) == 1`
- `len(Sessions) >= 1`
- 至少存在一个 final checkpoint

### 4.2 WorkspaceSnapshot

```go
type WorkspaceSnapshot struct {
    ID                 string
    VCSType            string // git
    Repository         RepositoryIdentity
    SourcePathHint     string
    GitCommonDirHint   string
    Branch             string
    HeadSHA            string
    UpstreamRef        string
    BaseSHA            string
    IsDetached         bool
    IsShallow          bool
    IsPartialClone     bool
    IsSparseCheckout   bool
    Submodules         []SubmoduleState
    Payload            WorkspacePayload
    Digest             WorkspaceDigest
}

type WorkspacePayload struct {
    CommitBundleObject string
    StagedPatchObject  string
    UnstagedPatchObject string
    UntrackedManifestObject string
}
```

### 4.3 AgentSession

```go
type AgentSession struct {
    ID              string
    Platform        string // codex, claude-code, cursor, generic
    NativeID        string
    NativeVersion   string
    AdapterVersion  string
    SourceCWDHint   string
    StartedAt       time.Time
    EndedAt         *time.Time
    RawObjects      []string
    NormalizedObject string
    Capabilities    AdapterCapabilities
}

type AdapterCapabilities struct {
    Archive       bool
    Normalize     bool
    NativeRestore bool
    Handoff       bool
}
```

### 4.4 NormalizedEvent

规范化层用于搜索、handoff 和 checkpoint 关联，不替代 raw session。

```json
{
  "schema_version": 1,
  "event_id": "evt_...",
  "session_id": "ses_...",
  "sequence": 42,
  "timestamp": "2026-07-30T06:00:00Z",
  "actor": "assistant",
  "kind": "tool_call",
  "summary": "Apply patch to parser",
  "payload": {
    "tool_name": "apply_patch",
    "status": "requested"
  },
  "checkpoint_id": "cp_...",
  "source": {
    "raw_object": "sha256:...",
    "record": 118
  }
}
```

允许的 MVP `actor`：

- `user`
- `assistant`
- `tool`
- `system`

允许的 MVP `kind`：

- `message`
- `tool_call`
- `tool_result`
- `decision`
- `file_change`
- `verification`
- `checkpoint`
- `error`

### 4.5 Checkpoint

```go
type Checkpoint struct {
    ID              string
    CreatedAt       time.Time
    Label           string
    WorkspaceID     string
    WorkspaceDigest WorkspaceDigest
    SessionPositions map[string]int64
}
```

MVP 只要求 capture 时生成 final checkpoint。后续版本可以通过 Agent hook 或文件系统事件生成中间 checkpoint。

## 5. Repository identity

路径和 branch 名不能作为仓库身份。

### 5.1 Remote normalization

规范化规则：

1. 删除 URL userinfo、access token 和密码。
2. 将常见 SCP 风格 `git@host:owner/repo.git` 转换为 `ssh://host/owner/repo`。
3. host 转为小写。
4. 删除尾部 `.git` 和多余 `/`。
5. 不删除 owner namespace。

### 5.2 Repository ID

优先使用：

```text
repo_id = sha256("git\0" + canonical_primary_remote + "\0" + root_commit)
```

无 remote 时：

```text
repo_id = sha256("git-local\0" + root_commit)
```

无 commit 的空仓库不属于 MVP，capture 必须拒绝并提示用户先创建初始 commit。

`root_commit` 仅作为消歧字段；catalog 必须缓存结果，避免每次扫描完整历史。

## 6. Workspace capture

### 6.1 Capture 前置条件

- 当前目录位于 Git worktree。
- 仓库至少有一个 commit。
- Git index 未被其他进程锁定。
- 当前 merge/rebase/cherry-pick 状态必须被检测并写入 manifest。
- 超出大小阈值时必须要求用户确认。

### 6.2 Git 状态采集

推荐命令：

```text
git rev-parse --show-toplevel
git rev-parse --git-common-dir
git rev-parse HEAD
git symbolic-ref --short -q HEAD
git rev-parse --abbrev-ref --symbolic-full-name @{upstream}
git status --porcelain=v2 -z
```

所有命令必须：

- 使用 argv 调用，不经过 shell 拼接；
- 强制 `LC_ALL=C`；
- 检查 exit code；
- 记录 Git 版本；
- 不把 remote credential 写入日志。

### 6.3 Base commit 选择

按以下顺序选择 `BaseSHA`：

1. upstream 存在时，使用 `merge-base(HEAD, upstream)`；
2. primary remote tracking ref 可推断时，使用其 merge-base；
3. 否则使用 root commit，并将 Capsule 标记为 `self_contained_commits=true`。

### 6.4 本地 commit bundle

当 `BaseSHA..HEAD` 非空时创建 bundle。bundle 中必须包含恢复 HEAD 所需的提交和 trees，但不应默认包含所有 branches/tags。

实现必须为 detached HEAD 创建临时内部 ref，完成 bundle 后删除临时 ref。临时 ref 名称：

```text
refs/sessionmgr/capture/<run-id>
```

如果没有本地 commits，则 `CommitBundleObject` 为空。

### 6.5 Staged 与 unstaged diff

分别执行：

```text
git diff --cached --binary --full-index --no-ext-diff
git diff --binary --full-index --no-ext-diff
```

diff 保存为两个独立 object，以便恢复 index 状态：

1. staged patch 使用 `git apply --index`；
2. unstaged patch 使用 `git apply`。

捕获时必须记录 executable bit、symlink 和 rename 信息。submodule dirty content 不进入普通 patch。

### 6.6 Untracked 文件

默认候选集合：

```text
git ls-files --others --exclude-standard -z
```

规则：

- `.git` 永远排除；
- socket、device、FIFO 永远排除；
- symlink 保存 link target，不跟随；
- 单文件和总大小受配置限制；
- 文件路径统一保存为 `/` 分隔的 repo-relative UTF-8 字符串；
- 非 UTF-8 路径在 MVP 中拒绝 capture，并给出报告；
- ignored 文件只能通过显式 include pattern 加入；
- 每个文件独立保存为 content-addressed object；
- manifest 记录 mode、size、mtime hint 和 digest。

### 6.7 Workspace digest

```go
type WorkspaceDigest struct {
    HeadSHA         string
    StagedPatchSHA  string
    UnstagedPatchSHA string
    UntrackedTreeSHA string
}
```

`UntrackedTreeSHA` 是按规范化路径排序后的 `(path, mode, object_digest)` 序列摘要，不依赖 mtime。

## 7. Codex adapter

### 7.1 Discovery

Codex state root：

1. 如果设置 `CODEX_HOME`，使用该目录；
2. 否则使用 `~/.codex`。

候选 session：

```text
$CODEX_HOME/sessions/**/*.jsonl
$CODEX_HOME/archived_sessions/*.jsonl
```

adapter 读取 session 第一条 metadata record，至少提取：

- native session ID；
- cwd；
- timestamp；
- source/originator；
- Codex version；
- model provider；
- Git metadata（如果存在）。

`session_index.jsonl` 仅用于补充 title，不作为 session 正文的唯一来源。

### 7.2 Session selection

默认选择策略：

1. `--session <id>`：精确匹配；
2. `--latest`：选择 canonical cwd 等于当前 worktree 的最新 session；
3. 多个候选且未指定时，进入 picker；
4. 非交互模式下歧义必须报错。

路径比较使用 canonical path，但不得把 canonical source path 当作目标机器恢复路径。

### 7.3 Raw capture

- 原始 JSONL 按字节复制；
- capture 前后读取文件 size 和 mtime；
- 如果文件在复制期间变化，重试一次；
- 第二次仍变化则拒绝，提示用户退出正在写该 session 的 Codex；
- 不复制 `auth.json`、SQLite、WAL、shell snapshots、logs 或 device state。

### 7.4 Normalization

Codex record type 到 `NormalizedEvent` 的映射由 versioned parser 完成。未知 record：

- 保留在 raw；
- 在 normalization report 中计数；
- 不因未知可选 record 使整个 capture 失败。

tool 参数默认只保存安全摘要。完整 tool payload 已存在 raw object 中，不重复进入 catalog。

### 7.5 Native restore

Codex 没有稳定的跨版本 session import contract，因此 MVP 将 native restore 标记为 `experimental`。

流程：

1. 目标设备必须自行完成 Codex 登录。
2. 检查目标 Codex 版本与 capture 版本。
3. 如果 native ID 已存在：
   - raw digest 相同：视为已导入；
   - raw digest 不同：拒绝覆盖，创建 handoff。
4. 把 raw session 写入目标 `CODEX_HOME/sessions/YYYY/MM/DD/`。
5. 以原子 rename 完成写入。
6. 合并 title index，不覆盖目标设备更新日期更晚的同 ID title。
7. 不复制 source 设备 SQLite。
8. 执行或提示：

```text
codex -C <restored-worktree> resume <native-session-id>
```

9. 如果目标 Codex 无法发现或恢复该 session，operation report 标记 `native_restore_failed`，并生成 handoff。

在 Codex session 重建索引行为完成 spike 验证前，该能力不得标记为 stable。

## 8. Capsule 格式

### 8.1 逻辑结构

Capsule 在 Store 中是 manifest 与 content-addressed objects 的集合：

```text
runs/<run-id>/manifest.json
objects/sha256/ab/cdef...
refs/runs/<run-id>
```

导出文件使用扩展名 `.smcap`：

```text
sessionmgr-capsule/
├── manifest.json
├── objects/
│   └── sha256/...
└── checksums.json
```

传输编码：

```text
deterministic tar → zstd → optional age encryption
```

### 8.2 Object descriptor

```json
{
  "digest": "sha256:abcdef...",
  "media_type": "application/vnd.sessionmgr.codex-session+jsonl",
  "size": 123456,
  "encoding": "identity",
  "required": true
}
```

### 8.3 Manifest 最小示例

```json
{
  "schema_version": 1,
  "run_id": "019fb197-fa7d-7aa1-ae70-43e8e9434c0d",
  "title": "Implement parser",
  "created_at": "2026-07-30T06:00:00Z",
  "parent_run_id": null,
  "relation": "capture",
  "created_by": {
    "machine_id": "machine:uuid:..."
  },
  "runtime": {
    "os": "darwin",
    "arch": "arm64",
    "git_version": "captured-at-runtime"
  },
  "workspaces": [],
  "sessions": [],
  "checkpoints": [],
  "objects": [],
  "security": {
    "scanner_version": "1",
    "blocked": 0,
    "warnings": 0
  },
  "capabilities": [
    "workspace.git.v1",
    "session.codex.raw.v1",
    "handoff.markdown.v1"
  ]
}
```

### 8.4 Canonical JSON

- UTF-8；
- object key 按字典序；
- 无无意义空白；
- 时间使用 UTC RFC 3339；
- digest 计算基于 canonical bytes；
- manifest 不把自身 digest 放入自身内容；
- refs 保存 manifest digest。

### 8.5 兼容性

- 相同 major schema：忽略未知可选字段；
- 未识别 `required=true` object media type：允许 inspect，不允许 restore；
- 未识别 required capability：拒绝 restore；
- schema major 不支持：拒绝并提示升级。

## 9. Local catalog

SQLite 只作为可重建索引，不是 Capsule 的 source of truth。

Run manifest 中的 canonical title 不可变。用户后续修改的显示名称和 tags 是 catalog overlay，不改写原 Capsule；MVP 中 overlay 仅在本地生效，后续版本可以把 overlay event 作为独立的不可变对象同步。

最小表：

```sql
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  manifest_digest TEXT NOT NULL,
  canonical_title TEXT NOT NULL,
  created_at TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  agent_platform TEXT NOT NULL,
  parent_run_id TEXT,
  relation TEXT NOT NULL,
  source_machine_id TEXT NOT NULL,
  native_session_id TEXT,
  integrity_status TEXT NOT NULL
);

CREATE TABLE run_overlays (
  run_id TEXT PRIMARY KEY,
  display_title TEXT,
  updated_at TEXT NOT NULL
);

CREATE TABLE tags (
  run_id TEXT NOT NULL,
  tag TEXT NOT NULL,
  PRIMARY KEY (run_id, tag)
);

CREATE TABLE path_mappings (
  repo_id TEXT NOT NULL,
  machine_id TEXT NOT NULL,
  local_path TEXT NOT NULL,
  last_verified_at TEXT,
  PRIMARY KEY (repo_id, machine_id)
);

CREATE TABLE sync_state (
  store_name TEXT NOT NULL,
  run_id TEXT NOT NULL,
  status TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (store_name, run_id)
);
```

catalog 可以通过扫描 `refs/runs` 与 manifest 完整重建。

## 10. Store 协议

### 10.1 Store interface

```go
type Store interface {
    HasObject(ctx context.Context, digest string) (bool, error)
    PutObject(ctx context.Context, desc ObjectDescriptor, r io.Reader) error
    GetObject(ctx context.Context, digest string) (io.ReadCloser, error)
    PutRefAtomic(ctx context.Context, name string, manifestDigest string) error
    GetRef(ctx context.Context, name string) (string, error)
    ListRefs(ctx context.Context, prefix string) ([]Ref, error)
}
```

### 10.2 发布顺序

Push 必须按以下顺序：

1. 上传缺失 payload objects；
2. 上传 manifest object；
3. 验证远端 objects 可读且 digest 一致；
4. 原子写入 `refs/runs/<run-id>`。

ref 发布前的 object 可由后续垃圾回收处理，不视为可见 Run。

### 10.3 File Store

- 使用同文件系统临时文件；
- `fsync` 文件；
- rename 到最终路径；
- `fsync` 父目录；
- 已存在 object 必须验证 size，必要时重新 hash；
- 不覆写不同内容的同 digest 文件。

### 10.4 SSH Store

- 使用 SFTP 或远程 helper；
- 上传到随机临时名；
- 完成后远程原子 rename；
- 不依赖远程 shell 字符串拼接；
- capability probe 必须确认 rename 和权限语义；
- 不支持可靠原子 rename 的后端不得用于 ref 发布。

### 10.5 Pull 与冲突

Run 与 object 不可变，因此 Pull 是集合并集。

同一个 `run_id` 指向不同 manifest digest 表示完整性或身份冲突：

- 两者均保留在 quarantine；
- catalog 不选择 winner；
- 用户必须运行 `sessionmgr reconcile`；
- 不允许 last-write-wins。

## 11. 加密

远程 Store 的 payload 使用 age recipient encryption。

规则：

- Store 只看到 ciphertext object；
- 明文 digest 保存在加密 manifest 内；
- 远程对象 key 使用 ciphertext digest，避免通过明文 digest 验证猜测内容；
- randomized encryption 下只保证本地明文对象去重；远程跨 Run 去重属于 best effort，不使用会泄露内容相等关系的确定性加密；
- identity 文件不上传；
- 本地 key 权限必须为 `0600`；
- 解密在本地临时目录中流式完成；
- 临时明文不得被 catalog 或日志引用。

MVP 不实现组织密钥轮换；重新加密产生新的远程 object set。

## 12. Secret scanning

### 12.1 扫描对象

- untracked/explicit ignored 文件；
- staged 与 unstaged 新增行；
- raw session 文本字段；
- handoff 输出。

### 12.2 默认阻断

- PEM private keys；
- OpenSSH private keys；
- 高置信度云服务访问 token；
- 含 credential 的 remote URL；
- 明确标记为密码或 secret 的 `.env` 键值。

输出只能包含：

- rule ID；
- 文件或 raw object；
- 行号/record；
- masked preview；
- severity。

不得把完整命中内容写入 report。

### 12.3 Override

本地 capture 可以带 warning 完成。远程 push 遇到 block finding 时默认拒绝。

允许：

```text
--exclude <path>
--allow-finding <finding-id>
```

`--allow-finding` 必须记录在 manifest security summary 中，但不记录秘密正文。

## 13. Restore 算法

### 13.1 前置检查

1. 验证 manifest schema；
2. 验证所有 required objects；
3. 解密并验证 checksum；
4. 定位 repo，或根据 canonical remote clone；
5. 确认目标路径不存在，或为空目录；
6. 检查文件系统大小写、symlink 和权限能力；
7. 检查 Git 版本与必要 capabilities。

### 13.2 默认 worktree 策略

默认路径：

```text
<repo-parent>/.sessionmgr-worktrees/<repo-name>/<run-id-short>
```

默认创建分支：

```text
sessionmgr/restore/<run-id-short>
```

如果原始 HEAD 已在本地可达：

```text
git worktree add -b <restore-branch> <target> <head-sha>
```

如果 HEAD 只存在于 bundle：

1. fetch bundle 到 `refs/sessionmgr/import/<run-id>`；
2. 基于该 ref 创建 worktree；
3. 保留 import ref，直到 restore 验证完成。

### 13.3 应用顺序

```text
commits
  → staged patch (`git apply --index --binary`)
  → unstaged patch (`git apply --binary`)
  → untracked files
  → mode/symlink verification
  → workspace digest verification
```

任何阶段失败：

- 停止后续阶段；
- 保留隔离 worktree供诊断；
- 输出 rollback/remove 命令；
- 不自动删除用户可能需要调查的数据；
- catalog 标记 restore failed。

### 13.4 路径安全

写入前必须拒绝：

- 绝对路径；
- 包含 `..` 的路径；
- Unicode normalization 后逃逸的路径；
- 目标目录外 symlink；
- 与 `.git` 冲突的路径；
- 大小写折叠后重复路径；
- Windows reserved names（即使当前平台不是 Windows，也记录兼容 warning）。

### 13.5 恢复完成

生成：

```text
operation-reports/<operation-id>.json
handoff/<run-id>.md
```

report 包含：

- restored run；
- target repo/worktree；
- created branch；
- workspace digest comparison；
- native session restore status；
- warnings；
- 推荐启动命令。

## 14. Handoff 生成

### 14.1 输入优先级

1. Git 与 operation facts；
2. structured normalized events；
3. raw session 引用；
4. 可选模型总结。

模型总结不是 MVP 必需依赖。无模型时必须生成确定性的事实模板。

### 14.2 Markdown 格式

```markdown
# Handoff

## Objective
## Current repository state
## Completed work
## Key decisions
## Changed files
## Verification performed
## Known issues
## Suggested next steps
## Provenance
```

规则：

- 没有证据的内容不得放入 Completed 或 Verification；
- 推断必须使用 “Inferred” 标签；
- 失败测试必须保留；
- source 路径必须映射到目标 worktree 或 repo-relative path；
- 不嵌入秘密正文。

## 15. CLI

二进制名称：`sessionmgr`。后续可以提供 `sm` alias，但不作为脚本稳定接口。

### 15.1 命令

```text
sessionmgr init
sessionmgr doctor [--json]

sessionmgr capture [--repo PATH]
                   [--agent codex]
                   [--session ID | --latest]
                   [--title TITLE]
                   [--untracked include|exclude]
                   [--include-ignored PATTERN]
                   [--json]

sessionmgr list [--repo REPO] [--agent PLATFORM]
                [--machine MACHINE] [--tag TAG] [--json]
sessionmgr show RUN_ID [--events] [--json]
sessionmgr verify RUN_ID [--deep] [--json]

sessionmgr restore RUN_ID [--repo PATH]
                  [--worktree PATH]
                  [--native-session]
                  [--json]

sessionmgr handoff RUN_ID [--to PLATFORM]
                  [--output PATH]
                  [--json]

sessionmgr push [RUN_ID] [--store NAME] [--json]
sessionmgr pull [--store NAME] [--json]
sessionmgr reconcile RUN_ID [--json]
```

互斥参数必须由 CLI parser 强制执行。非交互环境遇到未确认风险必须失败，不得假设 yes。

### 15.2 Exit codes

| Code | 含义 |
| --- | --- |
| 0 | 成功 |
| 2 | 参数或配置错误 |
| 3 | 环境/依赖检查失败 |
| 4 | capture 不完整或 session 正在变化 |
| 5 | security policy 阻断 |
| 6 | integrity 验证失败 |
| 7 | restore 冲突 |
| 8 | native restore 失败但 handoff 已生成 |
| 9 | Store/网络错误 |
| 10 | 不支持的 schema/capability |

### 15.3 JSON 输出

`--json` 输出单个结构化 result；进度写入 stderr。字段必须包括：

```json
{
  "schema_version": 1,
  "operation_id": "op_...",
  "status": "success",
  "run_id": "019f...",
  "warnings": [],
  "report_path": "/..."
}
```

## 16. Adapter API

MVP adapter 编译进二进制。外部插件协议后续定义，但内部接口现在保持边界：

```go
type AgentAdapter interface {
    ID() string
    Capabilities() AdapterCapabilities
    Discover(ctx context.Context, query SessionQuery) ([]SessionCandidate, error)
    Capture(ctx context.Context, candidate SessionCandidate, sink ObjectSink) (AgentSession, error)
    Normalize(ctx context.Context, session AgentSession, source ObjectSource, sink ObjectSink) (string, NormalizationReport, error)
    RestoreNative(ctx context.Context, session AgentSession, target RestoreTarget) (NativeRestoreReport, error)
    RenderHandoff(ctx context.Context, run Run, targetPlatform string) (HandoffArtifact, error)
}
```

adapter 不得直接写 catalog 或 Store refs；所有持久化由 Core Service 协调。

## 17. Operation 与并发

- 所有 mutation 分配 `operation_id`；
- 相同 worktree 同时只允许一个 capture/restore；
- 相同 Run 同时只允许一个 push 到同一 Store；
- lock 文件包含 PID、machine ID、started_at 和 operation ID；
- stale lock 必须经过进程检查与超时判断，不能仅按 mtime 删除；
- 接收到 SIGINT/SIGTERM 时停止新阶段、完成当前原子写入并生成 cancelled report；
- 不使用长期阻塞的全局锁。

## 18. 错误与降级

### 18.1 Capability 状态

每个操作结果必须使用以下状态之一：

- `supported`
- `experimental`
- `unsupported`
- `failed`
- `degraded`

例如 Codex native restore 失败但 handoff 成功：

```json
{
  "native_restore": "failed",
  "handoff": "supported",
  "overall": "degraded"
}
```

### 18.2 用户可操作错误

错误必须包含：

- 稳定 error code；
- 简洁说明；
- 受影响资源；
- 是否修改了状态；
- 下一步命令；
- report 路径。

## 19. 包结构建议

```text
cmd/sessionmgr/
internal/
├── app/
├── run/
├── catalog/
├── vcs/git/
├── adapter/
│   └── codex/
├── capsule/
├── store/
│   ├── file/
│   └── ssh/
├── crypto/
├── secretscan/
├── handoff/
├── operation/
└── testutil/
schemas/
├── manifest-v1.schema.json
└── normalized-event-v1.schema.json
fixtures/
├── codex/
└── git/
```

## 20. 测试策略

### 20.1 单元测试

- remote normalization；
- canonical JSON；
- digest 与 tree digest；
- path traversal 与 symlink 防护；
- secret masking；
- Codex record mapping；
- capability negotiation；
- lineage 和 ref conflict。

### 20.2 Git 集成测试矩阵

- clean branch；
- staged only；
- unstaged only；
- staged + unstaged 同文件；
- binary file；
- rename/delete；
- executable bit；
- symlink；
- untracked tree；
- ignored opt-in；
- local commits with upstream；
- detached HEAD；
- no upstream；
- linked worktree；
- shallow clone；
- sparse checkout；
- submodule warning；
- interrupted rebase/merge。

每个成功案例必须比较 capture 前与 restore 后的 `WorkspaceDigest`。

### 20.3 Adapter contract tests

每个 Codex fixture 记录：

- Codex version；
- raw JSONL digest；
- expected native ID/cwd/title；
- normalized event counts；
- unknown record counts；
- secret redaction expectations。

fixtures 不得包含真实用户 session 或秘密。

### 20.4 Store 故障注入

- object 上传中断；
- manifest 上传后 ref 前中断；
- ref rename 失败；
- 远端 object 损坏；
- 重复 push；
- 同 run ID 不同 manifest；
- 磁盘满；
- 权限变化。

### 20.5 安全测试

- tar path traversal；
- 绝对路径；
- symlink escape；
- 大小写碰撞；
- 压缩炸弹；
- 超大 manifest；
- 恶意 JSON 深度；
- 私钥/token fixtures；
- remote URL credential；
- 日志泄露检查。

## 21. 发布门槛

MVP 发布前必须满足：

1. PRD 的 AC-001 至 AC-005 自动化或半自动化通过。
2. 所有成功 restore 的 WorkspaceDigest 一致。
3. 所有 Capsule 可从空 catalog 重建索引。
4. 高置信度 secret fixtures 全部阻断远程 push。
5. Codex native restore 仍为 experimental，除非至少三个 Codex 版本的 fixtures 和真实隔离测试通过。
6. 中断 push 不产生可见半成品 Run。
7. CLI JSON schema 固定并有兼容性测试。
8. 安全解包测试覆盖所有已知路径逃逸类别。

## 22. 实现顺序

### Phase 1：Core 与 Git

- Run model、canonical manifest；
- object store、file backend；
- Git capture；
- restore 与 digest verification；
- operation report。

### Phase 2：Codex 与 Handoff

- session discovery 与 raw capture；
- normalization；
- deterministic Markdown handoff；
- local catalog。

### Phase 3：安全与跨机器

- secret scan；
- age encryption；
- SSH Store；
- push/pull 与故障恢复。

### Phase 4：Native restore spike

- Codex session import；
- title index merge；
- cwd mapping；
- 版本兼容矩阵；
- 明确 stable/experimental 判定。
