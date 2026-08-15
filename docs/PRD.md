# Session Manager v0.6 产品需求

## 1. 产品定义

Session Manager 是一个跨 macOS、Linux、Windows 的 Codex 与 DeepSeek Harness session
Markdown 导出器，同时提供 CLI 和本地 GUI。

Git 已经负责代码历史和跨机器同步。本产品只负责把分散在本机 agent harness 状态目录中的
sessions 转成可读、可由 Git 跟踪历史的文件。

## 2. 核心用户流程

### 首次使用

1. 用户启动 GUI 或运行 CLI 配置命令；
2. 选择一个导出目录；
3. Session Manager 创建目录并持久保存绝对路径；
4. 用户执行第一次导出；
5. UI 只显示这次创建或更新的 session 文档。

### 后续使用

1. Session Manager 自动恢复已配置目录；
2. 用户执行导出；
3. 系统默认扫描本机 active Codex sessions；用户可独立显式开启 Codex archived sessions 与
   DeepSeek Harness sessions；
4. 默认只导出 hosted Git sessions；用户显式开启时也包括非 Git/本地-only 目录；
5. hosted Git source 与已记录内容没有变化时不显示；非 Git目录每轮全量发布并显示；
6. 只显示本次新增、内容更新、名称变化或非 Git全量发布，并标明来源 harness。

## 3. 核心模型

```text
hosted Git remote key | device-local directory key
  -> set(device ID + harness + native session ID -> current Markdown + attachments)
```

- repository key 必须在同一 hosted repository 的 SSH/HTTPS clone 间稳定；
- 不允许用本机路径猜测跨机器仓库身份；
- 非 Git目录必须先把规范化绝对 CWD 哈希为 directory ID，再由 device ID 与 directory ID
  生成可重新验证的 key；key 只在该设备稳定，绝对路径不得进入导出文件；
- Codex session key 保持既有 device ID + native session ID 算法；DeepSeek Harness session key
  加入 harness discriminator，防止跨 harness 的 native ID 冲突；
- session 名称是可变的人类语义，只用于可见目录名而不承担身份职责；
- source hash 用于源变化检测，document hash 用于保护生成文件，二者均只进入隐藏 sidecar；
- 每个设备/session 只有一个 `conversation.md`，历史版本由 Git 保存；
- 多台机器按可读设备目录导出，结果通过普通 Git 合并。

## 4. 功能需求

### FR-1 持久目录

- GUI 和 CLI 必须共享一个 schema-versioned 配置；
- 配置文件必须使用操作系统标准配置目录；
- 用户指定不存在的导出目录时应安全创建；
- 下次启动必须自动恢复目录；
- 明确指定 `--directory` 时同时更新持久配置；
- 配置文件符号链接不得被跟随写入。
- 每台机器必须在本地配置中持久保存一个随机 device ID 与可读 device name；
- device identity 不得放在 Git 管理的导出根目录中，避免其他机器继承同一身份。

### FR-2 Session discovery

- 默认只扫描 Codex `sessions/` JSONL；CLI `--include-archived` 或 GUI 对应选项开启时才把
  `archived_sessions/` 加入 discovery；
- DeepSeek Harness 默认关闭；CLI `--include-deepseek` 或 GUI 对应选项开启时，扫描
  `$DSH_HOME/sessions`（默认 `~/.dsh/sessions`）下每个 session 目录唯一的
  `session.jsonl.zstd` 或 `session.jsonl`；
- DeepSeek compressed source 必须验证追加的 Zstandard frames、每 frame checksum、512 MiB
  解压上限与最终 JSONL 完整性；截断写入标记 `busy`，确定损坏或不支持格式标记 skipped；
- CLI `--include-non-git` 或 GUI 对应选项未开启时，可识别的非 Git/本地-only目录 session
  必须安静排除并通过 `filtered_non_git` 计数保持可观察；开启后才进入匹配与发布；
- 首次导出前已经位于 `archived_sessions/` 的 session 默认不得导出；显式包括 archived
  sessions 时必须按同一 native ID/device identity 处理；
- session 在成功导出后从 active 移入 `archived_sessions/` 时，默认导出不得因为 source
  未被扫描而修改或删除既有归档；
- source 后来从 active 与 archived 目录都消失时，普通导出不得删除、改名或修改已经归档的
  Markdown、附件、sidecar 或 derived catalog entry；source absence 不是删除指令；
- 支持全部仓库、当前/指定仓库、指定 session 三种范围；
- 全部候选文件必须共享一个短暂稳定窗口，而不是逐文件等待；
- 活跃文件在观察与读取前后必须保持 identity、size 和 mtime 一致；
- locked、变化中、被替换/移动或尾部 JSONL 不完整的文件必须标记 `busy`，本次静默忽略；
- `busy` 必须在 JSON 计数中可观察，但不得产生 warning 或非零退出码；
- 必须读取 `session_meta` 的 `originator`、`source`、`thread_source` 与
  `parent_thread_id`；Guardian/approval 和 thread-spawned subagent 默认不作为独立用户
  session 导出，JSON 结果通过 `filtered_internal` 计数保持可观察；
- DeepSeek header 必须读取 `origin`、`parentSession` 与 `delegationDepth`；任何 subagent
  provenance 都不得作为独立用户 session 导出；
- 不得修改、移动或删除 Codex 或 DeepSeek Harness 源文件与原生附件对象。

### FR-3 Repository identity

- hosted remote 去掉 scheme、Git username、HTTP credentials、query、fragment 和结尾
  `.git` 后作为身份输入；
- host 转小写，path 在 v1 中保留大小写；
- 空 remote、local path remote、`file://` remote 默认跳过；显式包括非 Git目录时，若 CWD
  仍是可访问目录，则必须走 device-local directory identity；
- repository identity 冲突不得覆盖已有文件。
- 可见 repository 路径直接位于导出根目录，使用
  `<host>-<owner-or-namespace>/<repo>`，不包含 `repositories/` wrapper 或 hash；
- host 与 Git owner/多级 namespace 必须合并为一个跨平台安全 component，例如
  `github.com-qiulinfan/sessionmgr` 与 `gitlab.com-team-platform/project`；
- hosted repository 的完整 key 与 canonical remote 必须保存在 `.sessionmgr-repository.json`。
- 非 Git目录可见路径必须是 `(non-git)<device-name>/<directory-name>`，session 直接位于
  directory identity 下且不得重复增加 device 目录；repository metadata
  使用 schema v2 保存 `local_directory` kind、可读目录名与设备 identity，但不得保存绝对 CWD；

### FR-4 Markdown export

- repository 目录之后必须直接是可读 device 目录，不得再增加 `sessions/` wrapper；
- 可见文档固定命名为 `conversation.md`，父目录使用创建时间与最新 session 名称；
- 设备、原生 session ID、session key、源 hash 与文档 hash 只保存在
  `.sessionmgr-session.json`；
- Markdown 保存 session 名称、来源 harness、设备显示名、可用的 Codex 版本和 Git 提示，
  不暴露 identity hash；
- 分别保存创建、首条消息、末条消息、末事件、标题更新和总体更新时间；
- 保存总消息、用户消息和助手消息数量；
- 保存用户与助手的可读对话；
- 新版 Codex 把任务启动上下文写成内部 `role=user` response 时，必须以
  `event_msg.user_message` 识别真实用户输入；未对应真实 user event 的插件列表、AGENTS
  规则、环境信息等注入内容不得进入标题或正文；
- `event_msg.user_message` 也可能由运行时合成，不得无条件视为用户输入。已知客户端说明
  必须按 session provenance 和完整固定前缀精确剥离；无 `client_id` 的已确认启动诊断必须
  屏蔽，而用户主动提交或引用相同诊断时必须保留；
- 标题只能来自顶层 session 的最新显式 index title，或净化后的第一条真实用户请求；不得
  从 subagent 继承的父历史、approval transcript、tool history、客户端说明或启动错误生成；
- 没有任何真实用户消息的 context-only source 不生成归档文档，也不得因此产生 warning
  或修改原始 JSONL；旧格式完全没有 user event 时必须继续兼容真实 response user message；
- 只归档用户通过聊天框结构化投入的附件；不得从消息正文、tool 参数/输出或 agent 读取过的
  路径推断附件；
- 单个附件原始内容不得超过 50 MiB（`50 * 1024 * 1024` bytes）；等于上限允许，超过
  上限时保留对话并在隐藏清单中记录 `too_large`，不得把该 session 整体判为失败；
- 已归档附件放在 session 目录的 `attachments/` 中，文件名使用稳定序号与可读原文件名，
  不含 hash；附件 hash、大小、MIME、来源类型、状态与消息位置保存在
  `.sessionmgr-session.json`；
- Codex 已内嵌在 session JSONL 中的附件 bytes 优先作为归档来源；只有旧格式仅保留本地
  路径时才执行稳定、no-symlink 的 best-effort 读取；不可读或正在变化的附件只产生 warning，
  不阻断对话导出，并在下次导出重试；
- 能证明与 session Git commit 中 tracked blob 完全一致的仓库内附件不重复复制，只在对话和
  sidecar 中记录 repository path/commit；其他附件不得因恰好位于工作树中而省略；
- 远程 URL 只记录为未归档 reference，禁止自动联网下载；
- 命中已知认证数据库、`.env`、private-key、token 或 secret assignment 形式的附件必须
  记为 `blocked_sensitive`，不复制 bytes，也不保存它的 content hash；
- 每条可读消息必须保留文件顺序与原始 timestamp；缺失时间不得用文件 mtime 猜测；
- tool 调用只保存数量，不保存参数或输出；
- developer/system 指令、内部 reasoning、认证数据和环境变量值不得进入导出；
- 常见私钥、token、credential URL 与 secret assignment 必须脱敏；
- malformed/omitted records 必须通过计数保持可见；
- DeepSeek event `seq` 必须连续，packed chunk row 必须验证成员数和时间；正文只保留
  `surfaceOp=append`、`source.kind=user` 的用户 message 与 model assistant 的可见 text，
  plugin/internal user injection、surface replacement、reasoning 与 tool payload 必须排除；
- DeepSeek 最新 `session/title` 用作标题；image block 只允许引用 DSH content-addressed object，
  复制前必须同时验证声明 SHA-256 与 byte size。

### FR-5 Incremental changeset

- 第一次看到某 session 时标记 `new`；
- source hash 变化时标记 `updated`；
- source hash 不变但显示名称变化时标记 `renamed`；
- source、标题、renderer 与生成内容均相同时必须是 no-op；
- 上述 no-op 只适用于 hosted Git sessions。选中的非 Git session 每轮必须重新解析、过滤、
  渲染、验证并发布；第一次标记 `new`，之后标记 `full`；
- 更新前必须验证当前 `conversation.md` 匹配 sidecar 的 document hash；人工编辑或
  identity/path collision 必须保留原文件并把该 source 作为 skipped 报告；
- 标题变化时，在所有权验证后重命名语义目录并更新同一个文档；
- CLI 和 GUI 默认只渲染当前操作创建的 changeset；
- changeset 为空时显示单一的 no-change 状态，不回显历史 catalog。
- 普通 export 只能新增或更新已发现的 session，不得通过“本次未发现”推导 tombstone、prune
  或删除操作；任何未来清理必须是独立、显式且可审阅的命令。
- `cleanup-internal` 必须默认 dry-run。只有仍存在且稳定的 raw source 结构化证明该 session
  是 internal/context-only，且当前设备 identity、repository/session sidecar、document
  hash、attachment hash 与目录全部归属 Session Manager 时，`--apply` 才能移除派生目录；
  未知文件、人工修改、symlink、source 缺失或 identity mismatch 必须阻止清理。

### FR-6 GUI

- 无参数启动程序时打开 GUI；
- GUI 必须由同一二进制提供，不依赖 Node 或平台 WebView SDK；
- 服务只能监听 loopback；
- 每次启动必须生成随机 API token；
- GUI 必须支持保存目录、系统目录选择、导出范围、包括 archived sessions、DeepSeek Harness
  sessions 与非 Git目录的独立显式勾选项，以及 changeset 展示；非 Git选项必须明确说明全量导出；
- hosted Git changeset 必须按 repository/device 目录分组，并可逐层展开或收起；非 Git
  repository 根已经包含 device scope，必须直接显示 session 叶节点，不得把 session 目录
  误作第二级 device folder；
- GUI 默认使用接近 GitHub Dark 的黑灰背景、surface、边框和状态色，不得回退为白底；
- GUI 必须提供 English/中文切换，首次加载默认 English，并在浏览器可用时记住用户选择；
- 桌面与窄屏布局必须可用；
- UI 不得执行 `git add`、commit 或 push。

### FR-7 CLI

- 必须提供 `config set-directory`、`config show`、`export`、`list`、`cleanup-internal`、
  `gui`、`version`；
- human output 与 JSON output 必须分离；
- `archive` 作为 `export` 兼容别名；
- `export --include-archived` 必须显式包括 Codex `archived_sessions/`，未传时只处理 active
  sessions；
- `export --include-deepseek` 必须显式包括 DeepSeek Harness sessions，未传时保持 Codex-only
  行为；`--deepseek-home` 可覆盖 `DSH_HOME`/`~/.dsh`；
- `export --include-non-git` 必须显式包括没有 hosted remote 的可访问 CWD；未传时不得发布；
- partial export 必须保留成功 changeset，同时以非零退出码和 warning 报告跳过项。

### FR-8 三系统分发

- 核心与 GUI 必须保持纯 Go、`CGO_ENABLED=0` 可构建；Zstandard 依赖必须是可固定版本且支持
  checksum 验证的纯 Go 实现；
- 必须能交叉构建 macOS、Linux、Windows；
- 目录打开/浏览使用各平台最接近的可用方式，并提供手工路径 fallback；
- 严格的 `vMAJOR.MINOR.PATCH` tag 必须在测试通过后自动创建 GitHub Release，至少附带
  Windows AMD64/ARM64 单文件 `.exe` 与 `SHA256SUMS.txt`；
- release binary 的 `sessionmgr version` 必须等于 tag 去掉 `v` 后的版本，开发构建继续明确
  显示 `-dev`；
- Windows release executable 必须包含已审阅的应用图标以及与 tag 一致的 file/product version
  resource；架构相关的中间 resource object 不进入 Git；
- release job 必须先执行 dependency verification、vet、普通测试与 race 测试，任一步失败时
  不得创建 Release；
- Windows release 的本地与 CI 构建必须复用同一个 PowerShell 实现，避免图标、版本、PE 与
  checksum 契约在两套脚本中漂移；
- tag version 必须匹配 source version，且同版本 devlog 必须已审阅并标记为 `Released`；
- Windows release 当前未签名时，下载说明必须明确 SmartScreen 风险，不得暗示已完成
  Authenticode 验证。

## 5. 非目标

- workspace capture/restore；
- Codex 或 DeepSeek Harness native resume/import；
- SQLite catalog；
- Capsule、加密、SSH Store 或自定义同步协议；
- 自动 Git commit/push；
- lossless replay 或完整 tool trace；
- 从自由文本猜测本地文件、归档 tool 输入/输出或 agent 自行读取的文件；
- 自动下载远程附件 URL；
- 公网 GUI 服务。

## 6. 验收标准

1. GUI 保存目录后重载仍显示该目录。
2. CLI 配置目录后，后续 `export` 无需再次传路径。
3. 同一 GitHub 仓库的 SSH/HTTPS sessions 进入同一 repository set。
4. 没有 hosted remote 的 session 默认被排除且不导致失败；显式包括非 Git目录后才导出。
5. hosted Git session 首次导出显示 `new`，重复导出只显示 no-change。
6. JSONL 追加后显示 `updated`。
7. 只重命名 session 后显示 `renamed`。
8. 同一 device/session 重复导出只保留一个 `conversation.md`；内容更新原地更新该文件。
9. GUI API 没有随机 token 时返回 unauthorized。
10. GUI 拒绝非 loopback listen address。
11. macOS、Linux、Windows no-CGO 构建全部通过。
12. 原始 Codex JSONL 与 DeepSeek session/attachment objects 在导出前后字节一致。
13. 稳定窗口内发生变化或尾部不完整的 session 只增加 `busy`，不生成文档且导出成功。
14. Markdown 明确区分创建、首次/最后对话与最后源事件时间，并为每条消息显示时间点。
15. 可见路径和 Markdown 文件名不包含 repository/session/content hash。
16. 隐藏 sidecar 中的 Codex session key 可由既有算法重新计算；DeepSeek key 可由 device ID、
    harness 与 native session ID 重新计算验证。
17. 手工改过的 `conversation.md` 与语义路径 identity collision 均不会被覆盖。
18. v1/v2 hash-named archive 仍可由 `list --history` 检查，但不会自动删除或改写。
19. 结构化聊天附件在 `attachments/` 中使用可读名称，并由隐藏 sidecar 的 SHA-256 保护。
20. 50 MiB 附件可导出；超过 50 MiB、忙碌、缺失或远程-only 的附件不会阻断其对话导出。
21. 普通消息中的路径、tool payload 和 agent 读取的文件不会被当作附件。
22. 新导出不创建 `repositories/`；GitHub 仓库可见路径为
    `github.com-<owner>/<repo>`。
23. 新导出在 `<repo>` 后直接创建 `<device-name>`，不创建 `sessions/` wrapper。
24. layout-v3/v4 current session 仍可列出，并只在安全校验后迁移到 layout v5。
25. GUI changeset 按 repository/device 两级目录分组，目录可展开和收起。
26. GUI 首次加载为英文，切换中文后静态文案与当前动态结果同步切换。
27. 同时含注入 `role=user` 上下文和真实 `user_message` event 的 Codex source 只导出真实
    对话；标题不得取自 `recommended_plugins`、`AGENTS.md` 或 `environment_context`。
28. context-only source 不创建文档；旧 renderer 污染文档在 ownership/hash 校验后升级到
    renderer v7、修复正文并按真实标题安全重命名。
29. 首次导出前已归档的 source 默认不创建文档，CLI/GUI 显式包括 archived sessions 后才
    导出；已导出的 source 从 active 移入 `archived_sessions/` 或完全消失后，既有 Markdown、
    sidecar bytes 和 list entry 均保持不变。
30. GUI 桌面与 390px 窄屏均使用 GitHub Dark 风格，表单和目录树无白色 surface，且没有
    横向溢出。
31. `source.subagent` 或 `thread_source=subagent` 的 Guardian 与 spawned worker 不创建独立
    文档，父 session 仍正常导出，JSON/GUI 可观察 filtered 计数。
32. PocketEngine 固定只读说明从正文和标题剥离而保留其后的真实请求；无 client ID 的纯
    MCP 启动错误不创建文档，用户主动提交同样文字时仍保留。
33. `cleanup-internal` dry-run 不改变任何文件；`--apply` 只清除完全验证的当前设备派生
    文档，人工编辑或额外文件会阻止清理，raw Codex JSONL 保持逐字节不变。
34. CLI `--include-non-git` 与 GUI 对应复选框都能导出真实存在、没有 hosted remote 的 CWD。
35. 非 Git目录第一次导出显示 `new`，重复执行显示 `full`，并继续拒绝覆盖人工修改。
36. 非 Git session 使用 `(non-git)<device>/<directory>/<session>` 可见布局，只出现一次
    device；绝对 CWD 不出现在 Markdown 或 repository/session sidecar 中。
37. 非 Git Guardian、spawned subagent、runtime context 与敏感内容仍经过和 hosted Git
    session 完全相同的过滤、脱敏与附件策略。
38. 非 Git source 后来消失时，既有全量归档仍保留，不推导删除或 tombstone。
39. GUI 中 hosted Git repository 保留 device folder；非 Git repository 下直接显示 session
    卡片，不为每个 session 生成 folder summary。
40. 未开启 DeepSeek 时，现有 Codex discovery、identity 与导出结果保持兼容；开启后 CLI 与 GUI
    可以导出真实 DSH format-v0 plain/compressed top-level session，并在 Markdown、sidecar、
    `list` 与 changeset 中标明 `deepseek` harness。
41. DeepSeek plugin user injection、subagent、surface replacement、reasoning 与 tool payload 不进入
    正文；append 的直接用户文本和 model assistant 文本保持 event 顺序。
42. DeepSeek 多 frame Zstandard source 的 checksum、event sequence 和 packed rows 均验证；截断
    frame/JSONL 记为 busy，checksum 损坏或 sequence discontinuity 不发布文档。
43. 同一设备上 Codex 与 DeepSeek 使用相同 native session ID、创建时间和标题时，session key 与
    可见语义目录仍不同且不得覆盖。
44. DeepSeek image object 只有在 path、声明 hash、声明大小和稳定读取全部一致时才可归档；重复
    导出是 no-op，原生 session 与 object bytes 均保持不变。
45. `v0.6.0` 形式的 tag 在所有发布门禁通过后生成 version-stamped Windows AMD64/ARM64 PE
    executables 和 `SHA256SUMS.txt`，并把三者附加到同一个 GitHub Release。
46. 本地 Windows release 脚本生成与 CI 相同命名和版本输出的两个 exe；当前机器架构对应的
    binary 实际执行 `version` 后必须精确报告所请求的版本。
47. 非 semver tag、source version/devlog 不匹配、依赖校验失败、vet/test/race 失败、非 PE 输出
    或 version stamp 不一致均阻止发布；发布说明明确当前 exe 未做 Authenticode 签名。
48. Windows AMD64/ARM64 release 均从同一透明 PNG 生成应用图标；构建后反向提取 executable
    resources，图标或 tag 对应 product version 缺失时阻止发布。
