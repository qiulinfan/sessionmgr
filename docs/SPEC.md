# Session Manager v0.5 技术规格

## 1. 进程与命令面

```text
sessionmgr                                      # GUI
sessionmgr gui [--listen 127.0.0.1:0] [--no-open]
sessionmgr config set-directory [--json] PATH
sessionmgr config show [--json]
sessionmgr export [--all | --repo PATH] [--session ID] [--include-archived]
                  [--include-non-git]
                  [--directory PATH] [--codex-home PATH] [--json]
sessionmgr list [--directory PATH] [--history] [--json]
sessionmgr cleanup-internal [--directory PATH] [--codex-home PATH]
                            [--apply] [--json]
sessionmgr version
```

`archive` 是 `export` 的兼容别名。`--output` 是不更新持久配置的一次性兼容 flag；
新调用应使用会保存目录的 `--directory`。

默认 source 为 `$CODEX_HOME`，未设置时是 `~/.codex`。`export` 默认处理全部 hosted
Git repositories；显式 `--repo` 时只处理该仓库。`--include-non-git` 开启后，all scope
也包括无法映射到 hosted remote 的可访问 CWD，显式 `--repo PATH` 也可直接指向这种目录。

普通 discovery 只扫描 `sessions/`。`--include-archived` 或 GUI request 的
`include_archived: true` 才把 `archived_sessions/` 加入同一次并集扫描。active/archived 是
Codex 的生命周期位置，不参与 Session Manager identity；显式包括时，同一 native session
移动目录后仍映射到同一个 device/session key。未在本轮 discovery 中出现的旧 archive
entry 不会生成 tombstone，也不会进入任何删除队列，因此已导出的 session 后来被归档或
删除 raw source 时，其派生文件保持不变。

非 Git目录默认通过 `filtered_non_git` 计数排除。`--include-non-git` 或 GUI request 的
`include_non_git: true` 才进入本机目录匹配。该选项不持久化，且不隐含 archived inclusion。

## 2. 持久配置 v1

```json
{
  "schema_version": 1,
  "export_directory": "/absolute/path",
  "device_id": "device:0123456789abcdef0123456789abcdef",
  "device_name": "workstation"
}
```

配置路径由 `os.UserConfigDir()` 提供，再追加 `sessionmgr/config.json`。环境变量
`SESSIONMGR_CONFIG` 可覆盖完整配置文件路径。

保存目录时：

1. 清理输入并转换成绝对路径；
2. `MkdirAll` 创建目录；
3. 验证目标确实是 directory；
4. 拒绝通过 config symlink 写入；
5. 以用户可读写权限写入并 `fsync`。

未知 config schema 或字段必须阻止写回，原文件保持可检查；未来新增 required
semantics 时增加 schema version。

`device_id` 在第一次导出时由 128-bit cryptographic random value 生成，之后由本机配置
稳定保存。`device_name` 默认来自 hostname。改变导出目录不得改变这两个字段；它们不能
存放在 Git 管理的导出目录中，否则新机器 pull 后会错误地继承旧机器身份。

## 3. Repository key v1

以下 remote：

```text
git@github.com:owner/repo.git
ssh://git@github.com/owner/repo.git
https://user:password@github.com/owner/repo.git?x=1
```

统一规范化为：

```text
github.com/owner/repo
```

key 为：

```text
sha256("git-remote-v1\0" + canonical_remote)
```

host 转小写；path 保留大小写；协议、userinfo、query、fragment 和 `.git` 不参与。
local/file/empty remote 不生成 hosted key。若 session metadata 没有 remote，转换器只允许从
其仍可访问的 CWD 查询 hosted `remote.origin.url`；仍没有则默认排除，显式 opt-in 时改用
下述 local directory key。

### 3.1 Local directory key v1

显式包括非 Git目录后，仍可访问但无法解析 hosted remote 的 CWD 使用：

```text
canonical_directory = Abs + EvalSymlinks(best effort) + Clean(CWD)
directory_id = sha256("local-directory-path-v1\0" + canonical_directory)
directory_key = sha256("local-directory-v1\0" + device_id + "\0" + directory_id)
```

该 key 只在当前 device identity 下稳定，不声称跨设备表示同一个目录。绝对 CWD 只作为
hash 输入，绝不写入 Markdown、repository sidecar、session sidecar 或 CLI/GUI changeset。
可见 repository name 为 `non-git:<directory-name>`，可见路径为
`non-git-<device-name>/<directory-name>`。目录名或设备名规范化碰撞由 hidden identity
检测并拒绝，不增加可见 hash 后缀。

## 4. Identity and change hashes / layout v5 / renderer v6

```text
source_hash = sha256(raw_jsonl_bytes)

session_key = sha256("device-session-v1\0" + device_id + "\0" + native_session_id)

document_hash = sha256(rendered_conversation_md_bytes)
```

三者职责不得混用：`session_key` 表达跨导出的稳定成员身份，`source_hash` 检测 Codex
原始数据变化，`document_hash` 验证准备更新的 Markdown 仍是 Session Manager 上次写入
的内容。renderer 产生影响 Markdown 的变化时必须增加 `RendererVersion`。

## 5. 文件布局与 schema

```text
<export-directory>/<host>-<owner-or-namespace>/<repo>/
├── .sessionmgr-repository.json
└── <device-name>/<created-time>--<session-title>/
    ├── .sessionmgr-session.json
    ├── conversation.md
    └── attachments/                  # only when archived bytes exist
        └── <sequence>-<readable-name>

<export-directory>/non-git-<device-name>/<directory-name>/
├── .sessionmgr-repository.json
└── <device-name>/<created-time>--<session-title>/
    ├── .sessionmgr-session.json
    ├── conversation.md
    └── attachments/
```

可见路径只承担语义：导出根目录下不存在 `repositories/` wrapper，repository 与 device
之间也不存在 `sessions/` wrapper。canonical remote 的
最后一段是 repository 名，其余 host + owner/多级 namespace 以 `-` 合并为第一层；
例如 `github.com/qiulinfan/sessionmgr` 得到 `github.com-qiulinfan/sessionmgr`。repository
namespace、设备名、UTC 创建时间和最新标题经过跨平台安全的 component 规范化，
每段最多 80 UTF-8 bytes。它不通过附加 hash 解决碰撞；若两个身份
规范化到同一路径，hidden metadata 必须发现 collision 并拒绝第二次写入。

hosted `.sessionmgr-repository.json` 包含 `schema_version`、`layout_version`、
`repository_key`、`repository_name` 与 `canonical_remote`。

hosted repository metadata 继续使用 schema v1。local-directory repository metadata 使用
schema v2：`canonical_remote` 为空，增加 `repository_kind=local_directory`、
`directory_name`、不可逆的 `directory_id`、`device_id` 与 `device_name`。schema v2 不保存
canonical/absolute CWD；reader 必须由 device ID 和 directory ID 重新计算 repository key，并
严格验证 kind、设备字段和 semantic path。session metadata、layout v5、renderer v6 与
attachment schema v1 不变。

`.sessionmgr-session.json` 包含 `schema_version`、`layout_version`、`renderer_version`、
repository identity、device ID/name、native session ID、session key、当前标题、source hash、
document hash、创建与更新时间，以及可选 attachments manifest。manifest 每项保存
message/attachment 序号、可读原名、MIME、来源类型、状态、相对归档路径、byte 大小和
content hash；不保存绝对本机路径、data URL 或带 credential/query 的远程 URL。它是可检查
的身份/所有权 sidecar，不是 secret store。

`conversation.md` 的 frontmatter 不包含 identity/hash。它保存 repository/device/session
显示名、Codex/Git hints，以及以下 renderer-v6 字段：

- `created_at`、`first_message_at`、`last_message_at`、`last_event_at`、
  `title_updated_at` 与用于排序的总体 `updated_at`；
- `source_records`、`malformed_records`、`omitted_records`、`tool_calls`、`messages`、
  `user_messages`、`assistant_messages`、`attachments`、`archived_attachments` 与
  `redactions` 计数。

时间字段没有可信源 timestamp 时省略。renderer v1 的 `started_at` 仍可由读取器检查；
renderer v2 不修改或删除任何既有 v1 文件。

## 6. Codex parsing

名称来自 `session_index.jsonl` 中同 ID 最大 `updated_at` record。没有名称时取第一条
经过下述 canonical selection 的真实用户消息的单行前 160 rune，再退回
`Codex session <ID>`。

顶层用户 session 由 `session_meta` provenance 判定。读取器解析 `originator`、`source`、
`thread_source` 与 `parent_thread_id`；`thread_source == "subagent"` 或结构化
`source.subagent`（包括 Guardian/approval 与 thread-spawned worker）默认标记为 internal，
不进入 repository matching、attachment capture 或 publication。该过滤不依赖标题文字，
也不影响其顶层 parent session。

新版 Codex 的主要 user-visible source 是 `event_msg.user_message`。同一 turn 的
`response_item.message(role=user)` 只在规范化正文与 event 相等时为该 event 补充更完整的
结构化附件；没有对应 user event 的 response user 不进入对话。这条规则过滤 Codex Desktop
以 user role 注入的 `recommended_plugins`、AGENTS instructions 和
`environment_context`，同时避免复制真实用户消息。

user event 仍需做 provenance-aware normalization：

- `originator=codex_exec`、`source=exec` 的 PocketEngine 已确认固定只读前缀按完整常量匹配
  并剥离，只保留其后的用户请求；其他来源或部分相似文本不处理；
- `originator=codex-tui`、`source=cli` 且没有 `client_id` 的已确认
  `MCP client for ... failed to start: MCP startup failed:` 启动诊断不视为用户请求；带
  `client_id` 的用户提交保留；
- 无 `client_id` 且完整匹配已知 context envelope 的合成 event 可过滤；普通提及、混合
  问题或带真实 client identity 的输入不得因关键词相似而删除。

assistant 优先使用 `response_item.message(role=assistant)`，完全没有 response assistant
时退回 `event_msg.agent_message`/`assistant_message`。user event 与所选 assistant message
按 JSONL record 顺序合并。旧 JSONL 完全没有 user event 时兼容 response user message，
但完整匹配已知注入 envelope 的 response 被排除。完全没有 canonical user message 的
context-only source 不发布 repository/session 文档，不计为失败；raw source 仍保持只读。
标题在 normalization 与 internal classification 之后生成：顶层 session 的最新 index title
优先，否则使用第一条净化后的真实 user message。internal session 不通过解析父 transcript
伪造“真实标题”。

tool arguments/results、developer/system message 和 reasoning payload 不进入正文。raw
bytes 保留在 Codex home；导出器只读源数据。

### 6.1 Structured chat attachments

附件只能来自 user message 的结构化字段。已确认的 Codex 形式是
`response_item.message.content` 中的 `input_image` / `input_audio`，以及 legacy
`event_msg.user_message` 的 `images` / `local_images` / `audio` / `local_audio`。读取器可
兼容结构化 `input_file`、`local_files`、`files` 和 `attachments`，但不得解析普通消息
文本中的路径。现代记录以 user event 为可见消息，并在正文匹配时优先采用 response 中的
embedded attachment bytes；旧记录只存在一种来源时直接使用该来源，附件不得重复。

单文件上限 `MaxAttachmentBytes = 50 * 1024 * 1024`；`size <= MaxAttachmentBytes`
允许，`size > MaxAttachmentBytes` 记为 `too_large`。data URL 在解码前先做编码长度
上界检查，解码器再通过 limit reader 强制上限；不得为判断超限而无界解码。

来源优先级与状态：

1. JSONL 内嵌 data URL：解码原始 bytes，状态 `archived`；
2. 结构化本地路径：`Lstat` 拒绝 symlink/非 regular file，以 identity/size/mtime
   前后检查稳定读取；忙碌或不可读时分别记 `busy` / `unavailable`；
3. 若有原始 path、session commit 与可访问 worktree，仅当附件 bytes 的 Git blob ID
   等于该 commit 中同一 repository-relative path 的 blob ID 时记 `git_tracked`，不复制；
4. HTTP(S) URL 记 `remote_reference`，不下载；
5. 超限记 `too_large`；命中已知认证数据库/`.env`/private-key/token/secret
   形式的内容记 `blocked_sensitive`，不写入原始 bytes 或 content hash。

`busy`、`unavailable`、`remote_reference`、`too_large` 和 `blocked_sensitive` 都是附件级
warning：对话文档仍
发布、命令仍成功，下次导出重试尚未 archived/git-tracked 的项。Markdown 在对应
user message 下用相对路径链接 `archived` 附件，其他状态只显示不含本机绝对路径
的说明。

`created_at` 来自 `session_meta`；`first_message_at`/`last_message_at` 是所选可读消息中
最早/最晚的原始 timestamp；`last_event_at` 是所有可解析源记录中的最大 timestamp。
消息正文保持 JSONL 文件顺序，标题格式为 `序号 · Role · timestamp`；无 timestamp
时只省略时间，不使用 filesystem metadata 补值。

## 7. Active-session stability

discovery 完成后，对所有候选文件执行一次批量观察：

1. 使用 `Lstat` 记录 regular-file identity、size 与 mtime；
2. 全部文件共享等待 350ms；
3. 再次记录 fingerprint；变化或消失的 source 记为 `busy`；
4. 打开稳定文件，并确认 handle identity 与观察对象相同；
5. 读取后同时检查 handle 与 pathname fingerprint；
6. 验证最后一个非空 JSONL record 是完整 JSON；
7. 任一步出现 source mutation、replacement、OS sharing/lock violation 或 incomplete
   tail 时记为 `busy`，不解析和发布。

该算法不主动申请文件锁。Unix advisory lock 不是可靠 liveness signal；Windows
sharing/lock violation 会显式映射为 `busy`。permission 和其他 I/O 错误仍是 `skipped`。
`busy` 不加入 warnings，也不使命令失败；human output 仍只显示 changeset，JSON result
增加 `busy` counter。

## 8. Incremental changeset

导出开始时一次读取隐藏 sidecar，并按 `repository_key + session_key` 建立 current map。
reader 同时扫描根目录两层的 layout-v4/v5 repository 与
`repositories/<host>/<owner>/<repo>` layout-v3 sidecar。repository 内同时识别
layout-v3/v4 的 `sessions/<device>/<session>` 与 layout-v5 的 `<device>/<session>`；旧
sidecar 已移动到 v5 目标但尚未完成 sidecar-last 更新时也保持可恢复。v1/v2 Markdown
frontmatter 只作为 legacy history 读取，不参与 current 写入身份。

成功发布后分类：

```text
current empty                                      -> new
current.source_hash == new.source_hash
  and current.title != new.title                   -> renamed
otherwise                                          -> updated
```

内容、标题、renderer 与 metadata 均相同时返回 unchanged，不进入 changeset。human CLI
不显示 hash 列；GUI 只显示 semantic path。扫描、matched、unchanged、busy、
`filtered_internal`、`filtered_non_git` 和 skipped 计数仍保留在 JSON result 供自动化诊断。

export result JSON 在 v0.5 使用 schema v2，增加 `filtered_non_git` 与 `full_exported`，并允许
change kind `full`。hosted Git session 保持上述增量规则。local-directory session 不使用
unchanged 快路：每次 opt-in export 都重新执行 canonical message selection、屏蔽、附件处理、
render、document/attachment ownership 验证和 sidecar-last 发布。第一次没有 current entry 时
仍标记 `new`；已有 current entry 时标记 `full` 并增加 `full_exported`。即使 bytes 相同也要
重新发布 owned document/sidecar，但不得加入 export timestamp 或其他制造 Git 内容差异的字段。

changeset 只由本轮发现并成功解析的 source 驱动。archive reader 在导出开始时读取的既有
entry 不会因为本轮没有对应 source 而被修改或删除；`List` 继续从隐藏 sidecar 派生该
entry。普通 export 不实现 mirror reconciliation、tombstone 或 prune。唯一的目录删除是
已验证 layout migration 后对确认空的旧 `sessions/`/device wrapper 调用非递归
`os.Remove`，它不能删除 session 文档或任何非空目录。未来如需清理必须设计独立显式命令。

local-directory 的“全量”同样只由本轮已发现 source 驱动，不是 mirror/prune。未发现的旧
local-directory entry 保持不变，不生成 tombstone，也不授权删除附件或 session directory。

### 8.1 Explicit internal cleanup

`cleanup-internal` 只清理由旧 renderer 错误发布、且仍能由当前设备的稳定 raw source 证明为
`subagent` 或已知 runtime-context-only 的 current document。默认 dry-run；`--apply` 是
独立、显式的删除授权，不改变普通 export 的 retention 语义。

候选必须依次通过：

1. raw source 经过与 export 相同的稳定窗口与完整 JSONL 检查；
2. raw session ID、当前配置 device ID、derived session key、repository key 与 sidecar
   identity 全部一致；
3. `conversation.md` hash 等于 sidecar `document_hash`；所有 archived attachment bytes
   等于 manifest size/hash；
4. session directory 只包含 conversation、session sidecar 和 manifest 声明的 attachment；
   任意额外文件、目录、symlink 或 unknown required metadata 都阻止删除；
5. 先把已验证目录原子移动到 export root 下的临时 recovery directory，再次验证后只按
   manifest 精确移除文件；失败时报告 recovery path，不递归删除未知内容。

source 缺失、busy、跨设备 entry 或无法证明 hosted repository identity 时不得成为候选。
raw Codex JSONL、repository sidecar、其他 device/session 和 legacy v1/v2 history 永不删除。

## 9. Owned-file update and publication

首次写入：

1. 先以 no-replace 语义发布 repository sidecar；
2. 创建 semantic session directory；
3. 同目录临时写入、`fsync` 后发布 `conversation.md`；
4. 最后发布 session sidecar，使 identity ref 不会早于 required document；
5. 已存在但没有 matching sidecar 的非空目录不得被认领或覆盖。

更新：

1. 严格读取 sidecar 并重新计算 device/session key；
2. 读取 `conversation.md`，其 hash 必须等于 sidecar `document_hash`；
3. 仅标题变化时先确认新 semantic directory 不存在，再 rename 当前目录；
4. 新文档写到同目录临时文件，`fsync` 后 replace；
5. 最后以新 document/source/title metadata replace sidecar；
6. 若上次在 document replace 后崩溃，当前文档恰好等于本次待写 bytes 时允许只修复 sidecar。

任何人工编辑、symlink、unknown required metadata、identity mismatch 或 semantic collision
均阻止该 session 更新，不静默覆盖用户文件。

## 10. Local GUI

GUI 使用标准库 `net/http` 和 `embed`：

```text
single binary
├── embedded HTML/CSS/JS
├── loopback HTTP server on random port
├── config API
├── export API
└── platform directory picker
```

启动时生成 256-bit random token，放在浏览器 URL fragment 中；fragment 不发送给 HTTP
server，前端把它放入 `X-Sessionmgr-Token` header。所有 `/api/*` 请求必须验证 token。

安全约束：

- `--listen` 只接受 loopback；
- request body 上限 1 MiB；
- JSON 拒绝未知字段；
- 静态页面设置 CSP、`nosniff`、`no-referrer` 和 `frame-ancestors 'none'`；
- 不提供 CORS；
- API 不执行 Git mutation；
- partial export 以 HTTP 200 返回成功 changeset，同时在 response `error` 字段中报告。

API：

- `GET /api/state`：当前持久目录；
- `PUT /api/config`：验证并保存目录；
- `POST /api/pick-directory`：调用平台目录对话框；
- `POST /api/export`：接受 `directory`、all/current `scope`、布尔值 `include_archived` 与
  `include_non_git`，执行对应范围并返回当前 changeset。两个 include 选项彼此独立且默认 false。

前端首次加载使用 English；用户可切换 English/中文，选择只保存在浏览器本地，不改变
跨机器 config schema。静态文案以及连接、保存、导出、busy/no-change、计数、change badge
与附件摘要等动态状态共享同一语言字典。changeset 在客户端按 `repository_key` 和
`device_name` 分成两级原生 `<details>` 目录树；repository/device summary 可独立展开，
session 变化作为对应 device 的叶节点显示。非 Git复选框必须明确标注 full export，`full`
change 使用独立双语 badge。后端 JSON changeset 保持扁平，避免为显示结构改变 CLI/API
contract。

默认视觉使用 GitHub Dark 风格的固定暗色 palette：页面 `#0d1117`、surface `#161b22`、
raised surface `#21262d`、border `#30363d`、正文 `#f0f6fc`，操作强调色使用 GitHub green。
浏览器 `color-scheme` 为 `dark`，原生 input/select 与滚动区域必须保持暗色；当前版本不提供
浅色主题切换。

## 11. 平台适配

| 能力 | macOS | Linux | Windows |
| --- | --- | --- | --- |
| 打开浏览器 | `open` | `xdg-open` | `rundll32` |
| 选择目录 | `osascript` | Zenity，回退 KDialog/手填 | PowerShell FolderBrowserDialog |
| 配置目录 | Application Support | XDG config | AppData |

核心只依赖 Go standard library 和运行时 Git。`make cross-check` 编译 darwin/arm64、
linux/amd64、windows/amd64；`make dist` 额外产出三系统 AMD64/ARM64 binaries。

## 12. 兼容性

v0.3 是从旧 Run/Capsule 产品有意重置的 breaking version，不读取旧 manifest、Store、
SQLite、encryption 或 GUI 状态。旧 `~/.sessionmgr` 保持原样。

同一 v0.3 development line 的早期 `archive --output` 用法继续工作，但默认目录现在来自
持久配置；首次使用必须通过 GUI、`config set-directory` 或 `export --directory` 指定。

archived discovery 的默认值从“总是包括”改为“显式 opt-in”。这不改变任何持久 schema、
identity 或现有归档 bytes；已存在但本轮未扫描到的 entry 继续保留。自动化若依赖旧行为，
必须增加 `--include-archived` 或在 GUI API request 中发送 `include_archived: true`。

v0.5 的 non-Git inclusion 同样默认关闭且按 operation opt-in。现有 hosted repository sidecar、
session schema、layout、renderer、附件和配置 bytes 不迁移。只有首次发布 local-directory
repository 时创建 repository metadata schema v2；v0.5 reader 同时支持 hosted schema v1 与
local schema v2。v0.4 binary 遇到 local schema v2 会 fail closed，但仍可读取未混入 local
directory sidecar 的旧 hosted archive。export JSON 从 schema v1 升为 v2，新增两个计数和
`full` change kind；调用方必须在升级后接受该 schema 才能使用 v0.5 automation。

layout v4 只改变 repository 可见路径；layout v5 进一步移除 repository 与 device 之间的
`sessions/` wrapper。schema v1、repository key、session key 与 attachment schema v1
不变。renderer v6 在 layout v5 内增加 session provenance、subagent exclusion 与来源限定的
user-event normalization；renderer-v4/v5 sidecar 仍可严格读取。下次导出先验证
旧 conversation/attachment hashes 与 identity，再将该 session directory rename 到
layout-v5 device 路径，最后发布 layout-v5 session sidecar。layout-v4 repository sidecar
与 v5 位于同一路径，只有完整 identity 一致时才原子升级；迁移后只删除确认为空的旧
device/`sessions` 目录。旧 renderer 污染文档同样只在 document/attachment ownership
验证后重写；标题变化时复用安全 semantic-directory rename。旧 layout-v3 repository
sidecar 和 v1/v2 hash-named 数据不自动删除；`list --history` 仍能检查它们。
内部旧文档只能由 dry-run-first `cleanup-internal --apply` 显式清理；其他旧归档的删除仍需
未来独立、可 review 的 migration。
