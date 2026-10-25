<h1 align="center">Session Manager<img src="assets/sessionmgr.png" alt="Session Manager icon" width="64"></h1>

Session Manager is a small tool I vibe coded to export Codex, Claude Code, and DsH sessions, thus communicating them between multiple agents and machines. It is simply a Go binary with a local browser UI and a CLI.

Example: You had a long codex session and 
- therefore the context has become messy
- or you are running low on Codex credits and want to continue the same work with another agent (such as Claude Code)
- or you want to move from one machine to another without losing the session history;
- or you want another agent as a judge to discuss the reasoning, attempts, and decisions the previous agent already made;

## Download

Windows users can download the portable executable from
[GitHub Releases](https://github.com/qiulinfan/sessionmgr/releases/latest):

- `sessionmgr-v1.0.2-windows-amd64.exe` for most Intel/AMD PCs
- `sessionmgr-v1.0.2-windows-arm64.exe` for Windows on ARM

Double-click the executable to start the local UI and open your default browser. Keep its console window open while using the app.

## Windows Setup

The downloaded EXE does not need Go, GNU Make, or an installer. Install Git for
repository detection:

```powershell
winget install --id Git.Git -e --source winget
```

If WinGet is unavailable, use the official [Git for Windows installer](https://git-scm.com/install/windows).
Run any supported harness at least once to create local sessions, then reopen
Session Manager. Its Environment panel checks Git and all three session directories.
The source switches at the top are enabled from this first detection and remain
under your control afterward.

## Build

Git, GNU Make, and Go 1.24+ on `PATH` are required.

```bash
make build
make check
make dist
```

On Windows, a release-equivalent pair of icon-bearing executables can be built with:

```powershell
.\scripts\build-windows-release.ps1 -Version 1.0.2
```

## Shell Usage

Launch the browser UI:

```bash
sessionmgr
```

Or configure and export from the CLI:

```bash
sessionmgr config set-directory /path/to/session-archive
sessionmgr export
```

Session Manager automatically scans available Codex, Claude Code, DeepSeek
Harness, Oh My Pi, and OpenCode state directories. The local GUI exposes five peer source switches at
the top; on first open, each detected source starts enabled and can be turned off.
Codex archived sessions and non-Git directories remain explicit policy choices:

```bash
sessionmgr export --include-archived
sessionmgr export --include-non-git
```

CLI exports only read sessions with activity in the last 30 days. Pick another
period with `--within`, or `--within all` for no limit; a session outside the
window is left for a later, wider export. `--session ID` ignores the window:

```bash
sessionmgr export --within 7d
sessionmgr export --within all
```

Use `--claude-home /path/to/claude-home` to override `CLAUDE_CONFIG_DIR` or
`~/.claude`.
Use `--deepseek-home /path/to/dsh-home` to override `DSH_HOME` or `~/.dsh`.
DeepSeek format 0 and 4 logs are supported; discovery selects the latest canonical
generation (`session.jsonl` or `session.vN.jsonl`, optionally `.zstd`) per session.
Unknown selected formats fail explicitly rather than falling back to older logs.

`export --gui-only` includes Codex Desktop, Claude Desktop roots, the Claude
Code sessions of the VS Code extension's side panel (entrypoint
`claude-vscode`), OpenCode Desktop local-project roots, and OMP sessions started
by Zed.
A Claude Desktop root counts only while Claude Desktop lists it unarchived: a
CLI run started inside a Desktop session inherits the `claude-desktop`
entrypoint, so the app's own session records decide membership, including the
CLI sessions a record replaced. They are `claude-code-sessions/**/local_*.json`
in the app's data folder, which it names Claude-Data, Claude-3p or Claude under
`%LOCALAPPDATA%` or `%APPDATA%` on Windows, `~/Library/Application Support` on
macOS and `~/.config` on Linux (override with `--claude-desktop-data`). No such
folder means no Desktop session is in scope; the receipt's
`claude_desktop_sessions` names the folders read. `--desktop-only` uses the same
list.
Zed launches OMP over ACP with `omp --session-dir <omp-home>/sessions-gui acp`;
GUI-only export reads only that directory (override with
`--omp-gui-session-dir`), never the CLI sessions in `<omp-home>/sessions`.
Claude Agent threads in Zed run the Claude Agent SDK, so their transcripts carry
the `sdk-ts` entrypoint. Zed deletes a Claude Agent transcript with its thread
but keeps an OMP session's file, so both count only while Zed's thread sidebar
lists them unarchived, under `claude-acp` or under the custom agent `OMP`
(default database `~/Library/Application Support/Zed/db/0-stable/db.sqlite` on
macOS or `%LOCALAPPDATA%\Zed\db\0-stable\db.sqlite` on Windows, override with
`--zed-db`). Other SDK sessions stay out. A root `claude-vscode` transcript
counts unless the extension archived it: VS Code's global state database
(`Code/User/globalStorage/state.vscdb` under `%APPDATA%`,
`~/Library/Application Support` or `~/.config`; override with `--vscode-state`)
lists archived side-panel sessions in the `hiddenSessionIds` of its
`Anthropic.claude-code` entry, and a Remote-WSL window keeps that entry in the
Windows database. The extension keeps no list of the sessions it started, so a
CLI run started inside a VS Code session counts too.
DeepSeek roots use a separate `export --gui-only=false --sources deepseek`.
A WSL distribution on a Windows machine shares the Windows host name; give its
own Session Manager home a distinct device name with
`sessionmgr config set-device-name NAME` (for example `qiulin-wsl`) so its
exports stay apart from the Windows ones.
Use `sessionmgr help` for all CLI options.

Each document's hash covers its exact bytes, so Git must not convert its line
endings. When the archive has no `.gitattributes`, export writes one with
`* -text`; Git for Windows enables `core.autocrlf` by default and would
otherwise check documents out with CRLF. An existing `.gitattributes` is left
as it is: an archive shared with Windows needs `* -text` there too.
