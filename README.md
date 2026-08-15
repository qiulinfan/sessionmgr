# sessionmgr

`sessionmgr` 把本机 Codex 与 DeepSeek Harness sessions 导出成可以由 Git 管理的 Markdown
文件。默认仍只扫描 Codex；DeepSeek Harness 与没有 hosted remote 的本地目录都需要显式
开启。

它的核心工作流有四部分：

1. 记住一个用户指定的导出目录；
2. 按规范化后的 Git 远程仓库组织 sessions，并可选择包括 DeepSeek Harness 与非 Git/本地-only
   目录；
3. Git 仓库只显示增量变化，非 Git目录每次显式执行全量导出；
4. 通过默认 dry-run 的显式命令，安全清理由旧 renderer 误导出的内部 session 副本。

Codex home 与 DeepSeek Harness home 对 Session Manager 始终是只读源：程序只扫描和读取
原始 JSONL/Zstandard 日志及显式引用的附件对象，不会写入、重命名、归档或删除它们。程序
的写操作只发生在自己的配置文件和用户指定的导出目录；`cleanup-internal --apply` 也只作用于
经过身份与 hash 校验的 Codex 导出副本。

## GUI

直接运行程序或执行：

```bash
sessionmgr
# 等价于
sessionmgr gui
```

程序会在随机 loopback 端口启动本地页面并打开默认浏览器。GUI 可以：

- 选择、保存并恢复导出目录；
- 导出全部目录或当前目录的 sessions，并可分别勾选包括 Codex 已归档 sessions、DeepSeek
  Harness sessions 与非 Git目录；非 Git选项明确标记为全量导出；
- 按 repository 与设备目录折叠展示本次新增、更新或重命名的 Markdown 文档，并标出
  该变化中的附件/复制数；
- 非 Git根节点已经表示设备，其变化直接显示为 session 卡片，不为每个对话创建重复的
  文件夹层；
- 使用接近 GitHub Dark 的黑色界面，表单、状态和目录树保持统一暗色层级；
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

默认导出全部能够识别 hosted Git remote 的 active sessions。也可以缩小范围：

```bash
sessionmgr export --repo /path/to/repo
sessionmgr export --session <native-session-id>
```

默认不会导出已经位于 Codex `archived_sessions/` 的 session。需要显式包括它们时运行：

```bash
sessionmgr export --include-archived
```

DeepSeek Harness 默认不扫描。需要把 `$DSH_HOME/sessions`（未设置时 `~/.dsh/sessions`）中的
顶层会话加入同一次导出时运行：

```bash
sessionmgr export --include-deepseek
sessionmgr export --include-deepseek --deepseek-home /path/to/dsh-home
```

`--include-deepseek`、`--include-archived` 与 `--include-non-git` 相互独立；例如 DeepSeek
会话的 CWD 没有 hosted remote 时，还需要同时传 `--include-non-git`。

默认也不会导出无法映射到 hosted Git remote 的 session。需要把非 Git目录或只有本地 Git
历史、没有 hosted origin 的目录一并全量导出时运行：

```bash
sessionmgr export --include-non-git
sessionmgr export --repo /path/to/non-git-directory --include-non-git
```

非 Git目录每次都会重新解析、过滤、渲染并安全发布全部匹配 session。第一次显示 `NEW`，
之后显示 `FULL`；所有权/hash 校验仍会阻止覆盖人工修改或冲突文件。未再次发现的旧归档也
不会被删除。

临时指定并持久保存一个新目录：

```bash
sessionmgr export --directory /new/archive/path
```

CLI 的人类输出只包含本次 changeset：`NEW`、`UPDATED`、`RENAMED`，以及非 Git目录的
`FULL`。只有 hosted Git sessions 且重复执行没有变化时输出：

```text
No changes.
```

`export`、`config`、`list`、`cleanup-internal` 支持 `--json`。`archive` 仍作为 `export`
的兼容别名。
人类可读的 CLI 表格不显示 hash；完整 identity/change hash 仅在 JSON 和隐藏 sidecar
中供校验与自动化使用。

默认导出只保留顶层用户 session。Codex 的 Guardian/approval 与 spawned subagent 会根据
`session_meta.source`/`thread_source` 结构化识别；DeepSeek Harness 会根据 header 中的
`origin`、`parentSession` 与 `delegationDepth` 识别 subagent。它们都由
`filtered_internal` 计数显示。清理由旧 renderer 错误导出的 Codex 内部文档时，先运行：

```bash
sessionmgr cleanup-internal --directory /path/to/session-archive
# 审阅 dry-run 后再显式应用
sessionmgr cleanup-internal --directory /path/to/session-archive --apply
```

清理只处理仍有稳定原始 source、属于当前设备、且 sidecar/document/attachment hash 与
目录所有权全部验证通过的派生文档；原始 Codex JSONL 不会被删除。普通 `export` 仍不执行
任何删除。

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

<configured-directory>/
└── (non-git)<device-name>/
    └── <directory-name>/
        ├── .sessionmgr-repository.json
        └── <created-time>--<session-title>/
            ├── .sessionmgr-session.json
            ├── conversation.md
            └── attachments/
```

- repository key 来自去掉协议和凭据后的 hosted Git remote；同一仓库的 SSH 与
  HTTPS clone 使用同一个键。
- 导出根目录下直接是 `<host>-<owner-or-namespace>/<repo>`；不再有 `repositories/`
  wrapper，host 与用户/多级 namespace 合并为一层，例如
  `github.com-qiulinfan/sessionmgr`。
- hosted repository 后直接是 `<device-name>`；当前布局不再创建多余的 `sessions/`
  wrapper。非 Git根目录已经包含设备名，因此 session 直接放在目录身份下面，不重复设备层。
- 没有 hosted remote 的 session 默认不导出；`--include-non-git` 或 GUI 对应选项开启后，
  按其稳定 CWD 与当前 device ID 组成的本机目录身份全量导出。
- 非 Git身份的绝对路径只参与本机 hash 计算，不进入 Markdown 或隐藏 sidecar。可见路径用
  `(non-git)<device-name>/<directory-name>`；同名规范化碰撞仍拒绝覆盖。
- 每台机器首次导出时在本地配置中生成持久 device ID；Codex session 继续沿用 device ID 与
  原生 session ID 的既有 key，DeepSeek Harness key 额外包含 harness identity，防止两个
  harness 的同名原生 ID 冲突。
- hash 不出现在可见目录或 Markdown 文件名中。repository/session identity、source hash
  和 document hash 分别保存在两个隐藏的 `.sessionmgr-*.json` 文件中。
- 每个设备/session 只有一个 `conversation.md`；内容更新时安全更新它，名称改变时重命名
  语义目录，旧版本由 Git 历史保存。
- DeepSeek Harness 的语义 session 目录带 `deepseek--` 前缀，使相同创建时间和标题也不会与
  Codex 可见路径碰撞；Markdown、sidecar、`list` 与 changeset 都显式记录 `harness`。
- 用户在聊天框中结构化投入的图片、音频与可识别文件会跟随对话导出。可见
  文件名使用稳定序号和可读原名，hash、大小、MIME、状态与消息位置只保存在
  `.sessionmgr-session.json`。
- 单附件上限是 50 MiB（包含恰好 50 MiB）。超限、忙碌、缺失、远程-only 或疑似
  credential/private-key 的文件不会被复制，但不会阻断对话导出；当次状态通过
  Markdown、hidden manifest 和 warning 说明。
- 仅当附件 bytes 能证明等于 session 记录 commit 中的 tracked Git blob 时才不重复复制。
  普通消息里的路径、tool payload 和 agent 自行读取的文件不会被猜测为附件；
  HTTP(S) 引用也不会被自动下载。
- hosted Git session 重复导出相同内容是 no-op；选中的非 Git session 每次显示 `FULL`。
  不同机器仍按可读设备目录产生文件，可由普通 Git 合并。
- 更新前会校验 document hash；手工改过的 Markdown 或语义目录 identity collision 不会
  被覆盖，而会作为 skipped 项提示。

v0.3 开发早期产生的 hash-named v1/v2 文件仍可用 `sessionmgr list --history` 查看。
layout-v3 的 `repositories/<host>/<owner>/<repo>` 与 layout-v4 的
`<host>-<namespace>/<repo>/sessions` current documents 也仍可读，并在下次经过
所有权/hash 校验的导出时移到上面的 layout-v5 路径。确认没有其他内容后，迁移会移除空的
旧 `sessions/`/device 目录；程序不会自动删除旧 repository sidecar 或 v1/v2 归档。

导出不会锁住原生源文件。所有 Codex 与已选择的 DeepSeek source 共用一次短暂稳定观察窗口；
在窗口内仍在变化、读取时被替换、被操作系统报告为锁定，或尾部记录不完整的 session 会
记入 JSON 结果的 `busy` 计数并留到下次处理。DeepSeek `.jsonl.zstd` 支持当前 harness 的
多 frame 追加格式；每个 frame 的结构、checksum、解压上限和最终 JSONL 完整性都会验证。
`busy` 不产生 warning 或失败退出码，确定的损坏或不支持格式则作为 skipped 报告。

普通导出只扫描 Codex active `sessions/`；`--include-archived` 或 GUI 的对应选项才会把
`archived_sessions/` 加入同一次扫描。因此，在首次导出前已经被用户归档的 session 默认
不会进入导出目录。一个已经导出的 session 后来被 Codex 归档或源文件消失时，普通导出也
不会删除、改名或截断它已有的 Markdown、附件或隐藏 sidecar。显式包括 archived sessions
时，同一原生 session 仍映射到原来的 device/session identity，并可安全更新。Session
Manager 是追加/更新式归档器，不把 Codex 当前目录镜像成需要删除同步的 catalog；内部污染
记录只能由 dry-run-first `cleanup-internal` 显式删除，其他历史归档不会被普通导出清理。

非 Git全量导出只改变“匹配 source 每轮都重新发布并显示”的策略，不授予删除权限，也不改变
上述 archived/source-missing retention 规则。

DeepSeek Harness discovery 只接受每个 session 目录中的一个 `session.jsonl.zstd` 或
`session.jsonl`。header 必须是 format v0 的顶层会话；event `seq` 必须连续，packed chunk
记录也要通过成员与时间校验。正文只投影 `surfaceOp=append` 且 `source.kind=user` 的真实
用户消息，以及 model assistant 的可见 text block；plugin 注入、surface replacement、
reasoning 和 tool payload 不进入正文。最新 `session/title` 作为标题。图片引用只从
`attachments/v1/objects` 读取，并在复制前同时验证声明的 SHA-256 与大小。

每个 Markdown 文档包含明确的时间轴：创建时间、第一条和最后一条可读消息时间、最后
一条源事件时间、标题更新时间，以及用户/助手消息数量。正文保持源文件顺序，并在每条
消息标题上显示其原始 UTC 时间；源记录没有 timestamp 时不会使用文件时间猜测。

新版 Codex 可能把 `recommended_plugins`、`AGENTS.md` 和 `environment_context` 等运行
上下文存成内部 `role=user` response。Session Manager 以实际 `user_message` event 识别
用户输入，只从匹配的 response 补充结构化附件；这些注入内容不会进入标题或正文。完全
没有真实用户对话的 context-only source 保持原始 JSONL 不变，但不创建归档文档。

并非所有 `user_message` 都来自用户：内部 Guardian 会把父会话 transcript 与 tool history
包装成审批输入，spawned subagent 会继承父会话历史，某些客户端还会把编辑器说明或 MCP
启动错误写入 user event。renderer v7 使用 session provenance 排除所有 subagent，对已知
客户端前缀做来源限定的精确剥离，并只在无 `client_id` 的合成事件中屏蔽已确认的运行诊断。
标题仅来自顶层 session 的显式索引或净化后的第一条真实用户请求。

renderer v7 首次重导旧文档时会产生一次 changeset：可修复的客户端前缀标题会在
所有权/hash 校验后显示为 `RENAMED`，其他需要升级 renderer 的文档显示为 `UPDATED`。
内部/context-only 旧文档必须通过上面的显式 dry-run-first 清理移除；完成后重复导出恢复
为 no-op。

## 内容与安全边界

Markdown 保存用户/助手对话、完整消息时间轴、Git commit/branch 和少量计数。用户明确
投入的附件保留原始 bytes，因此同样需要在公开提交前审阅。它不复制
developer/system 指令、tool 参数、tool 输出、认证数据库、内部 reasoning 或环境变量
值，也不复制 Codex 为任务启动注入的插件、仓库规则、运行环境上下文或 DeepSeek plugin
user messages；常见 token、
私钥、credential URL 和 secret assignment 会替换成明确的
`[REDACTED ...]`。

原始 Codex JSONL 与 DeepSeek session/attachment objects 始终只读并保留在各自 home。
生成内容仍应在提交到公开 Git 仓库前人工审阅，因为自由文本可能包含无法自动识别的敏感信息。

## 构建与验证

要求 Git 和 GNU Make。仓库会优先使用 `PATH` 中已有的 Go；如果没有找到 Go，
构建包装器会从 `go.dev` 下载仓库固定的便携版本，验证 SHA-256 后解压到被 Git
忽略的 `.tools/go/<os>-<arch>/`。它不会修改系统安装或全局 `PATH`。可以显式完成
这一步，也可以让第一次构建自动执行：

```bash
make bootstrap
```

Windows 自动引导使用 PowerShell；macOS/Linux 使用 `curl` 或 `wget`、`tar`，以及
`sha256sum` 或 `shasum`。已有 Go 必须为 1.24 或更高版本，也可以通过
`make build GO=/path/to/go` 显式覆盖。普通本机构建可在 PowerShell、Windows
Command Prompt 或 POSIX shell 中运行：

```bash
make build
```

`make clean` 保留下载缓存，避免反复获取工具链；需要明确删除所有仓库本地工具链
和下载缓存时运行 `make clean-tools`，下次构建会重新验证并下载。

产物在 Windows 上是 `bin/sessionmgr.exe`，在 macOS/Linux 上是
`bin/sessionmgr`。完整验证与分发构建使用：

```bash
make check
make dist
```

`make check` 包含 `go test -race ./...`；Windows 上运行该项还需要 Go CGO
支持的 C 编译器。其他 build/test/vet/cross-check/dist 目标保持
`CGO_ENABLED=0`。

`make dist` 生成 macOS、Linux、Windows 的 AMD64/ARM64 单文件程序。仓库内也保留
一个把导出目录设为本仓库 `sessions/` 的便捷脚本：

```bash
./scripts/export-codex-sessions
```

产品契约见 [PRD](./docs/PRD.md)，格式与算法见 [SPEC](./docs/SPEC.md)，工程证据见
[v0.6 devlog](./docs/devlogs/v0.6.0-dev.md)。
