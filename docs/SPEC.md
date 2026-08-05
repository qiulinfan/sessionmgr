# Session Manager v0.3 技术规格

## 1. 进程与命令面

```text
sessionmgr                                      # GUI
sessionmgr gui [--listen 127.0.0.1:0] [--no-open]
sessionmgr config set-directory [--json] PATH
sessionmgr config show [--json]
sessionmgr export [--all | --repo PATH] [--session ID]
                  [--directory PATH] [--codex-home PATH] [--json]
sessionmgr list [--directory PATH] [--history] [--json]
sessionmgr version
```

`archive` 是 `export` 的兼容别名。`--output` 是不更新持久配置的一次性兼容 flag；
新调用应使用会保存目录的 `--directory`。

默认 source 为 `$CODEX_HOME`，未设置时是 `~/.codex`。`export` 默认处理全部 hosted
Git repositories；显式 `--repo` 时只处理该仓库。

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
local/file/empty remote 不生成 key。若 session metadata 没有 remote，转换器只允许从
其仍可访问的 CWD 查询 hosted `remote.origin.url`；仍没有则跳过。

## 4. Identity and change hashes / renderer v3

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
<export-directory>/repositories/<host>/<owner>/<repo>/
├── .sessionmgr-repository.json
└── sessions/<device-name>/<created-time>--<session-title>/
    ├── .sessionmgr-session.json
    └── conversation.md
```

可见路径只承担语义：remote 各段、设备名、UTC 创建时间和最新标题经过跨平台安全的
component 规范化，每段最多 80 UTF-8 bytes。它不通过附加 hash 解决碰撞；若两个身份
规范化到同一路径，hidden metadata 必须发现 collision 并拒绝第二次写入。

`.sessionmgr-repository.json` 包含 `schema_version`、`layout_version`、`repository_key`、
`repository_name` 与 `canonical_remote`。

`.sessionmgr-session.json` 包含 `schema_version`、`layout_version`、`renderer_version`、
repository identity、device ID/name、native session ID、session key、当前标题、source hash、
document hash、创建与更新时间。它是可检查的身份 sidecar，不是 secret store。

`conversation.md` 的 frontmatter 不包含 identity/hash。它保存 repository/device/session
显示名、Codex/Git hints，以及以下 renderer-v3 字段：

- `created_at`、`first_message_at`、`last_message_at`、`last_event_at`、
  `title_updated_at` 与用于排序的总体 `updated_at`；
- `source_records`、`malformed_records`、`omitted_records`、`tool_calls`、`messages`、
  `user_messages`、`assistant_messages` 与 `redactions` 计数。

时间字段没有可信源 timestamp 时省略。renderer v1 的 `started_at` 仍可由读取器检查；
renderer v2 不修改或删除任何既有 v1 文件。

## 6. Codex parsing

名称来自 `session_index.jsonl` 中同 ID 最大 `updated_at` record。没有名称时取第一条
用户消息的单行前 160 rune，再退回 `Codex session <ID>`。

消息优先读取 `response_item` 中 role 为 `user`/`assistant` 的 message；旧记录退回
`event_msg` 的 `user_message`、`agent_message`、`assistant_message`。两种来源不会同时
输出，以免重复。

tool arguments/results、developer/system message 和 reasoning payload 不进入正文。raw
bytes 保留在 Codex home；导出器只读源数据。

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
v1/v2 Markdown frontmatter 只作为 legacy history 读取，不参与 v3 写入身份。

成功发布后分类：

```text
current empty                                      -> new
current.source_hash == new.source_hash
  and current.title != new.title                   -> renamed
otherwise                                          -> updated
```

内容、标题、renderer 与 metadata 均相同时返回 unchanged，不进入 changeset。human CLI
不显示 hash 列；GUI 只显示 semantic path。扫描、
matched、unchanged、busy 和 skipped 计数仍保留在 JSON result 供自动化诊断。

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
- `POST /api/export`：执行 all/current scope 并返回当前 changeset。

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

layout/renderer v3 不自动迁移、删除或改写 v1/v2 的 hash-named repository/snapshot 文件。
`list --history` 仍能检查旧 frontmatter；第一次 v3 导出会另建 semantic current document。
旧归档的删除必须留给一个未来的显式、可 review migration。
