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
- 只显示本次新增、更新或重命名的 Markdown 快照；
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

## 持久配置

配置使用 schema v1 JSON，位置来自 Go 的系统标准配置目录：

- macOS：`~/Library/Application Support/sessionmgr/config.json`
- Linux：`$XDG_CONFIG_HOME/sessionmgr/config.json`，通常是 `~/.config/sessionmgr/config.json`
- Windows：`%AppData%\sessionmgr\config.json`

测试或便携环境可以用 `SESSIONMGR_CONFIG=/custom/config.json` 覆盖配置文件位置。

## 文件模型

```text
<configured-directory>/
└── repositories/
    └── <repo-name>--<repository-sha256>/
        ├── repository.md
        └── sessions/
            └── <codex-session-id>/
                └── <session-title>--<snapshot-sha256>.md
```

- repository key 来自去掉协议和凭据后的 hosted Git remote；同一仓库的 SSH 与
  HTTPS clone 使用同一个键。
- 没有 hosted remote 的 session 不会被猜测归类，而是明确跳过并给出 warning。
- session 内容更新或名称改变时新增一个不可变 hash 文件。
- 相同快照重复导出是 no-op；不同机器产生的文件可由普通 Git 合并。
- 文件名保留当前 session 标题，完整 hash 负责唯一性。

## 内容与安全边界

Markdown 保存用户/助手对话、时间、Git commit/branch 和少量计数。它不复制
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
