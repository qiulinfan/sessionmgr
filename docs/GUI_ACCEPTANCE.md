# Session Manager GUI 验收计划

> 目标版本：`v0.2.0-dev`
> 关联规划：[GUI_IMPLEMENTATION_PLAN.md](./GUI_IMPLEMENTATION_PLAN.md)

## 1. 验收原则

- 默认使用完全隔离的 acceptance sandbox；
- 不读取真实 `~/.codex`、真实 `~/.sessionmgr` 或真实 SSH Store；
- 每个 mutation 必须同时检查 UI 结果和 operation report；
- 以 workspace digest、object checksum 和 Git status 为事实依据；
- native resume 未通过版本矩阵前只能验收 experimental/degraded 行为；
- “按钮可点”不是通过标准，必须检查最终系统状态。

## 2. 验收环境

运行：

```bash
make gui-acceptance
```

预期命令创建：

```text
acceptance-sandbox/
├── sessionmgr-home/
├── codex-home/
├── remote.git/
├── source/
├── target-repo/
├── file-store/
└── evidence/
```

Source fixture 必须包含：

- remote baseline commit；
- 至少一个 unpushed commit；
- 同一文件同时有 staged 和 unstaged 修改；
- binary staged 修改；
- executable bit 修改；
- symlink；
- ordinary untracked 文件；
- explicit ignored 文件；
- 一个高置信度 private-key fixture；
- 不包含任何真实 secret 的 Codex JSONL fixture。

每次验收记录：

- Git commit；
- GUI build identifier；
- OS/arch；
- sandbox path；
- operation IDs；
- reports；
- screenshots；
- expected/actual digests；
- Pass/Fail 与说明。

## 3. 核心验收矩阵

### GUI-AC-001：首次启动与初始化

步骤：

1. 使用空的 `SESSIONMGR_HOME` 启动 GUI；
2. 查看 Setup；
3. 点击 Initialize；
4. 重新打开 GUI。

通过条件：

- 首次启动不崩溃；
- 明确显示 Git/Codex/age checks；
- 初始化生成 machine ID 和 age identity；
- UI 只显示 public recipient；
- 重启后进入 Overview；
- home/key 权限符合要求；
- 不创建遥测请求。

### GUI-AC-002：Run Catalog 和详情

步骤：

1. 打开预置多个 Run 的 sandbox；
2. 按 repo、Agent、machine、integrity 筛选；
3. 打开 Run Detail；
4. 浏览所有 tabs。

通过条件：

- 筛选结果与 catalog query 一致；
- 大量 events 使用分页，不一次加载全部 raw；
- manifest、workspace digest、session capability 和 security summary 可见；
- raw session 默认不加载；
- experimental 能力有文字标识。

### GUI-AC-003：Capture 预检不修改状态

步骤：

1. 打开 Capture Wizard；
2. 选择 source repo 和 fixture session；
3. 完成预检但不确认；
4. 关闭 Wizard。

通过条件：

- 显示 commit/staged/unstaged/untracked/ignored 清单；
- 显示 payload 大小和 warnings；
- 显示 secret scan 结果；
- 没有发布 Run ref；
- source Git status 没有变化；
- 临时 capture ref 不残留。

### GUI-AC-004：Capture 输入变化保护

步骤：

1. 完成 Capture 预检；
2. 在外部修改 workspace 或 session；
3. 点击确认执行。

通过条件：

- 旧 plan 被拒绝；
- UI 提示输入已变化并要求重新预检；
- 不发布半成品 Run；
- report 说明未修改可见状态。

### GUI-AC-005：完整 Capture

步骤：

1. 重新预检；
2. 确认 capture；
3. 打开完成页和 Run Detail。

通过条件：

- Run ref 最后发布；
- raw session checksum 与 fixture 一致；
- staged 和 unstaged patch 分离；
- unpushed commit bundle 存在；
- ordinary untracked 和 explicit ignored 均归档；
- UI 摘要与 manifest 一致；
- operation report 状态 success。

### GUI-AC-006：Deep Verify

步骤：

1. 对正常 Run 执行 deep verify；
2. 在另一个 sandbox 破坏一个 object；
3. 再次 deep verify。

通过条件：

- 正常 Run 显示 verified；
- 损坏 Run 显示 failed，而不是 warning；
- UI 显示 digest，不显示 object 内容；
- catalog integrity 状态更新；
- report 给出下一步。

### GUI-AC-007：跨仓库 Restore

前置：

- target repo 只包含 remote baseline，不包含 source 的 unpushed commit。

步骤：

1. 打开 Restore Wizard；
2. 选择 target repo；
3. 使用建议的隔离 worktree；
4. 不启用 native restore；
5. 执行。

通过条件：

- commit bundle 导入成功；
- 创建 `sessionmgr/restore/<run>` branch；
- staged/unstaged 同文件状态保持；
- binary、mode、symlink、untracked、ignored 恢复；
- expected digest 等于 actual digest；
- target 原 worktree 不变；
- handoff 自动生成。

### GUI-AC-008：Restore 冲突保护

步骤：

1. 将目标路径准备为非空目录；
2. 执行 restore。

通过条件：

- restore 在写入前停止；
- 原文件字节不变；
- UI 状态 failed；
- report 标记 state modified 为 false；
- 提供选择新目录的 action。

### GUI-AC-009：Native Restore 降级

步骤：

1. 启用 experimental native restore；
2. 使用不可导入或同 ID 不同内容 fixture；
3. 执行 restore。

通过条件：

- workspace restore 仍成功；
- native restore 显示 failed；
- overall 显示 degraded；
- handoff 存在且可打开；
- UI 不使用完整绿色 success；
- 不覆盖已有 native session。

### GUI-AC-010：Handoff 事实边界

步骤：

1. 打开没有 verification event 的 fixture Run；
2. 预览并保存 handoff。

通过条件：

- 文档包含 Objective、Git state、Changed files 和 Provenance；
- 明确说明没有结构化验证证据；
- 不声称测试通过；
- inferred 和 suggested 有标记；
- 无 secret 正文。

### GUI-AC-011：SSH Push 安全阻断

步骤：

1. 打开包含 private-key fixture 的 Run；
2. 选择 SSH Store；
3. 尝试 Push。

通过条件：

- Push 在网络连接前被阻止；
- UI 显示 rule、文件和 masked preview；
- 不显示 secret 正文；
- 提供排除文件并重新 capture 的建议；
- Store 不出现 ref。

### GUI-AC-012：File Store 跨 home 同步

步骤：

1. 从 machine A sandbox Push 到 file Store；
2. 使用 machine B sandbox Pull；
3. 打开、verify 并 restore。

通过条件：

- Pull 是集合并集；
- 本地独有 Run 保留；
- 同 object 不重复复制；
- pulled Run deep verify 通过；
- restore digest 一致。

### GUI-AC-013：SSH 中断与重试

步骤：

分别在以下阶段注入断线：

1. ciphertext upload 中；
2. upload 后 verify 前；
3. verify 后 ref publish 前。

通过条件：

- 中断 Run 不在远程列表出现；
- Retry 复用有效 ciphertext/cache；
- ref 只在远程 checksum 通过后发布；
- 最终 Pull/verify 成功；
- `.tmp` ref 不被当成 Run。

### GUI-AC-014：取消操作

步骤：

1. 在可取消的长阶段点击 Cancel；
2. 在原子 publish 阶段点击 Cancel。

通过条件：

- 可取消阶段安全停止并生成 cancelled report；
- 原子阶段完成原子边界后再停止；
- 不留下可见半成品 Run；
- UI 不假装立即取消；
- Retry action 与实际状态一致。

### GUI-AC-015：应用重启和 operation catch-up

步骤：

1. 启动长 operation；
2. 关闭/重开窗口或 reload 前端；
3. 打开 Operations。

通过条件：

- 后端 operation 不因 UI reload 损坏；
- UI 从 sequence/report 恢复最新状态；
- 不重复显示 event；
- 完成结果和 report 可见。

### GUI-AC-016：可访问性和键盘

步骤：

1. 不使用鼠标完成 Capture Wizard；
2. 不使用鼠标打开 Run、Verify 和 Restore preflight；
3. 设置 200% zoom；
4. 使用 screen reader smoke。

通过条件：

- focus 顺序合理且可见；
- modal 能正确 trap/restore focus；
- 状态不只通过颜色表达；
- 关键操作有 accessible name；
- 200% zoom 无确认内容丢失；
- destructive/experimental confirmation 可被朗读。

### GUI-AC-017：Production 安全配置

步骤：

1. 构建 production GUI；
2. 检查资源和网络；
3. 尝试打开 devtools、remote URL 和未绑定 method。

通过条件：

- 无 remote script/font/image；
- CSP 生效；
- production devtools 默认不可用；
- 未绑定 method 不可调用；
- no localhost listener；
- 无 telemetry；
- 日志不包含 session 正文和 secrets。

## 4. 阶段验收门槛

| Phase | 必须通过 |
| --- | --- |
| Phase 0 | AC-001 的 backend 部分、service contract、CLI regression |
| Phase 1 | AC-001、002、006、015 的只读部分 |
| Phase 2 | AC-003、004、005、011 |
| Phase 3 | AC-007、008、009、010 |
| Phase 4 | AC-012、013、014、015 |
| Phase 5 | AC-016、017，以及全部核心回归 |

## 5. 验收证据模板

```markdown
# GUI Acceptance Evidence

- Version:
- Commit:
- Build:
- OS/Arch:
- Date:
- Sandbox:

## Scenario

- Acceptance ID:
- Result: Pass / Fail
- Operation IDs:
- Expected digest:
- Actual digest:
- Reports:
- Screenshots:

## Notes

- State modified:
- Warnings:
- Unverified assumptions:
```

任何失败用例都必须保留现场或记录可复现步骤，不能仅附截图。
