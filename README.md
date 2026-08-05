# sessionmgr

`sessionmgr` 把本机 Codex sessions 导出成可以由 Git 管理的 Markdown 文件。

它只做三件事：

1. 记住一个用户指定的导出目录；
2. 按规范化后的 Git 远程仓库组织 sessions；
3. 每次只显示这次真正导出的变化。

## GUI

直接运行程序或执行：

```bash
sessionmgr
# 等价于
sessionmgr gui
```

程序会在随机 loopback 端口启动本地页面并打开默认浏览器。GUI 可以：

- 选择、保存并恢复导出目录；
- 导出全部 Git 仓库或当前 Git 仓库的 sessions；
- 按 repository 与设备目录折叠展示本次新增、更新或重命名的 Markdown 文档，并标出
  该变化中的附件/复制数；
- 在 English/中文之间切换；首次使用默认英文，选择会保存在本机浏览器中；
- 没有变化时显示明确的“已经是最新状态”。

目录选择器在 macOS 使用系统对话框，在 Windows 使用 Folder Browser，在 Linux
优先使用 Zenity 或 KDialog；任何平台都可以直接输入路径。

GUI 只监听 `127.0.0.1`/`::1`，API 使用每次启动随机生成的 token。页面和 API 都
内嵌在同一个 Go 二进制中，不需要 Node、WebView 或单独的服务。

## CLI

配置一次导出目录：

```bash
sessionmgr config set-directory /path/to/session-archive
sessionmgr config show
```

以后直接导出：

```bash
sessionmgr export
```

默认导出全部能够识别 hosted Git remote 的 sessions。也可以缩小范围：

```bash
sessionmgr export --repo /path/to/repo
sessionmgr export --session <codex-session-id>
```

临时指定并持久保存一个新目录：

```bash
sessionmgr export --directory /new/archive/path
```

CLI 的人类输出只包含本次 changeset：`NEW`、`UPDATED`、`RENAMED`。重复执行且没有
变化时只输出：

```text
No changes.
```

`export`、`config`、`list` 支持 `--json`。`archive` 仍作为 `export` 的兼容别名。
人类可读的 CLI 表格不显示 hash；完整 identity/change hash 仅在 JSON 和隐藏 sidecar
中供校验与自动化使用。

## 持久配置

配置使用 schema v1 JSON，位置来自 Go 的系统标准配置目录：

- macOS：`~/Library/Application Support/sessionmgr/config.json`
- Linux：`$XDG_CONFIG_HOME/sessionmgr/config.json`，通常是 `~/.config/sessionmgr/config.json`
- Windows：`%AppData%\sessionmgr\config.json`

测试或便携环境可以用 `SESSIONMGR_CONFIG=/custom/config.json` 覆盖配置文件位置。

## 文件模型

```text
<configured-directory>/
└── github.com-<owner>/
    └── <repo>/
        ├── .sessionmgr-repository.json
        └── <device-name>/
            └── <created-time>--<session-title>/
                ├── .sessionmgr-session.json
                ├── conversation.md
                └── attachments/
                    └── 001-<readable-file-name>
```

- repository key 来自去掉协议和凭据后的 hosted Git remote；同一仓库的 SSH 与
  HTTPS clone 使用同一个键。
- 导出根目录下直接是 `<host>-<owner-or-namespace>/<repo>`；不再有 `repositories/`
  wrapper，host 与用户/多级 namespace 合并为一层，例如
  `github.com-qiulinfan/sessionmgr`。
- repository 后直接是 `<device-name>`；当前布局不再创建多余的 `sessions/` wrapper。
- 没有 hosted remote 的 session 不会被猜测归类，而是明确跳过并给出 warning。
- 每台机器首次导出时在本地配置中生成持久 device ID；session key 由 device ID 与
  Codex 原生 session ID 共同生成。
- hash 不出现在可见目录或 Markdown 文件名中。repository/session identity、source hash
  和 document hash 分别保存在两个隐藏的 `.sessionmgr-*.json` 文件中。
- 每个设备/session 只有一个 `conversation.md`；内容更新时安全更新它，名称改变时重命名
  语义目录，旧版本由 Git 历史保存。
- 用户在聊天框中结构化投入的图片、音频与可识别文件会跟随对话导出。可见
  文件名使用稳定序号和可读原名，hash、大小、MIME、状态与消息位置只保存在
  `.sessionmgr-session.json`。
- 单附件上限是 50 MiB（包含恰好 50 MiB）。超限、忙碌、缺失、远程-only 或疑似
  credential/private-key 的文件不会被复制，但不会阻断对话导出；当次状态通过
  Markdown、hidden manifest 和 warning 说明。
- 仅当附件 bytes 能证明等于 session 记录 commit 中的 tracked Git blob 时才不重复复制。
  普通消息里的路径、tool payload 和 agent 自行读取的文件不会被猜测为附件；
  HTTP(S) 引用也不会被自动下载。
- 重复导出相同内容是 no-op；不同机器按可读设备目录产生文件，可由普通 Git 合并。
- 更新前会校验 document hash；手工改过的 Markdown 或语义目录 identity collision 不会
  被覆盖，而会作为 skipped 项提示。

v0.3 开发早期产生的 hash-named v1/v2 文件仍可用 `sessionmgr list --history` 查看。
layout-v3 的 `repositories/<host>/<owner>/<repo>` 与 layout-v4 的
`<host>-<namespace>/<repo>/sessions` current documents 也仍可读，并在下次经过
所有权/hash 校验的导出时移到上面的 layout-v5 路径。确认没有其他内容后，迁移会移除空的
旧 `sessions/`/device 目录；程序不会自动删除旧 repository sidecar 或 v1/v2 归档。

导出不会锁住 Codex 的源文件。所有 JSONL 共用一次短暂稳定观察窗口；在窗口内仍在
变化、读取时被替换、被操作系统报告为锁定，或尾部记录不完整的 session 会记入 JSON
结果的 `busy` 计数并留到下次处理。`busy` 不产生 warning 或失败退出码。

每个 Markdown 文档包含明确的时间轴：创建时间、第一条和最后一条可读消息时间、最后
一条源事件时间、标题更新时间，以及用户/助手消息数量。正文保持源文件顺序，并在每条
消息标题上显示其原始 UTC 时间；源记录没有 timestamp 时不会使用文件时间猜测。

## 内容与安全边界

Markdown 保存用户/助手对话、完整消息时间轴、Git commit/branch 和少量计数。用户明确
投入的附件保留原始 bytes，因此同样需要在公开提交前审阅。它不复制
developer/system 指令、tool 参数、tool 输出、认证数据库、内部 reasoning 或环境变量
值；常见 token、私钥、credential URL 和 secret assignment 会替换成明确的
`[REDACTED ...]`。

原始 Codex JSONL 始终只读并保留在 Codex home。生成内容仍应在提交到公开 Git
仓库前人工审阅，因为自由文本可能包含无法自动识别的敏感信息。

## 构建与验证

要求 Go 1.24 或更高版本，以及 Git：

```bash
make check
make dist
```

`make dist` 生成 macOS、Linux、Windows 的 AMD64/ARM64 单文件程序。仓库内也保留
一个把导出目录设为本仓库 `sessions/` 的便捷脚本：

```bash
./scripts/export-codex-sessions
```

产品契约见 [PRD](./docs/PRD.md)，格式与算法见 [SPEC](./docs/SPEC.md)，工程证据见
[v0.3 devlog](./docs/devlogs/v0.3.0-dev.md)。
