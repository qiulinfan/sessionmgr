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

- `sessionmgr-v0.6.0-windows-amd64.exe` for most Intel/AMD PCs
- `sessionmgr-v0.6.0-windows-arm64.exe` for Windows on ARM

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
.\scripts\build-windows-release.ps1 -Version 0.6.0
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

Session Manager automatically scans available Codex, Claude Code, and DeepSeek
Harness state directories. The local GUI exposes three peer source switches at
the top; on first open, each detected source starts enabled and can be turned off.
Codex archived sessions and non-Git directories remain explicit policy choices:

```bash
sessionmgr export --include-archived
sessionmgr export --include-non-git
```

Use `--claude-home /path/to/claude-home` to override `CLAUDE_CONFIG_DIR` or
`~/.claude`.
Use `--deepseek-home /path/to/dsh-home` to override `DSH_HOME` or `~/.dsh`.
Use `sessionmgr help` for all CLI options.
