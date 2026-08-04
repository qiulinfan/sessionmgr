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
  "export_directory": "/absolute/path"
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

## 4. Snapshot hash / renderer v2

```text
source_hash = sha256(raw_jsonl_bytes)

snapshot_hash = sha256(
  "sessionmgr-markdown-v2\0" +
  repository_key + "\0" +
  source_hash + "\0" +
  redacted_display_title + "\0" +
  title_updated_at
)
```

renderer 产生影响既有 Markdown 的变化时必须增加 `RendererVersion`。

## 5. 文件布局与 schema

```text
<export-directory>/repositories/<repo-slug>--<full-repository-hash>/
├── repository.md
└── sessions/<native-session-id>/<title-slug>--<full-snapshot-hash>.md
```

不安全的 native ID 改用 ID hash 作为路径。slug 最多 80 UTF-8 bytes，完整 hash 负责
唯一性。所有文件是 UTF-8 Markdown，frontmatter 使用 YAML-compatible JSON quoted
scalars。

`repository.md` schema v1：`schema_version`、`repository_key`、`repository_name`、
`canonical_remote`。

snapshot schema v1：repository/session identity、snapshot/source hash、Codex/Git hints，
以及以下 renderer-v2 字段：

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

导出开始时一次读取目标目录内已有 snapshot frontmatter，并按
`repository_key + session_id` 建立 history map。

新 snapshot 成功发布后分类：

```text
history empty                                      -> new
latest.source_hash == new.source_hash
  and latest.title != new.title                    -> renamed
otherwise                                          -> updated
```

publish 返回 unchanged 时不进入 changeset。human CLI 与 GUI 只遍历 `changes[]`；扫描、
matched、unchanged、busy 和 skipped 计数仍保留在 JSON result 供自动化诊断。

## 9. Immutable publication

1. 在目标目录创建临时文件；
2. 写入、`fsync`、关闭；
3. 使用 hard link 以 no-replace 语义发布；
4. `EEXIST` 时读取比较，相同视为 unchanged，不同视为 conflict；
5. 删除临时名字。

该流程不静默覆盖现有 snapshot、repository descriptor 或用户文件。

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

renderer v2 为既有 source 产生新的 immutable snapshot hash；第一次使用 v2 时，旧 v1
session 可能各新增一个 `updated` changeset。旧文件仍可列出且不会被重写。
