<p align="center">
  <img src="assets/sessionmgr.png" alt="Session Manager icon" width="128">
</p>

<h1 align="center">Session Manager</h1>

<p align="center">
  Export local Codex and DeepSeek Harness sessions as readable, Git-friendly Markdown.
</p>

Session Manager is a single Go binary with a local browser UI and a CLI. It keeps
native session data read-only, groups exports by repository and device, and safely
updates only files it owns.

## Download

Windows 10/11 users can download the portable executable from
[GitHub Releases](https://github.com/qiulinfan/sessionmgr/releases/latest):

- `sessionmgr-v0.6.0-windows-amd64.exe` for most Intel/AMD PCs
- `sessionmgr-v0.6.0-windows-arm64.exe` for Windows on ARM

Double-click the executable to start the local UI and open your default browser.
Keep its console window open while using the app.

The release also includes `SHA256SUMS.txt`. Verify a download in PowerShell with:

```powershell
Get-FileHash .\sessionmgr-v0.6.0-windows-amd64.exe -Algorithm SHA256
Get-Content .\SHA256SUMS.txt
```

The Windows binaries are not Authenticode-signed, so SmartScreen may show an
unknown-publisher warning. Confirm that the file came from the official release
page and that its SHA-256 matches before running it.

## Use

Launch the browser UI:

```bash
sessionmgr
```

Or configure and export from the CLI:

```bash
sessionmgr config set-directory /path/to/session-archive
sessionmgr export
```

By default, Session Manager exports active Codex sessions associated with a hosted
Git remote. Extra sources are explicit:

```bash
sessionmgr export --include-archived
sessionmgr export --include-deepseek
sessionmgr export --include-non-git
```

Use `--deepseek-home /path/to/dsh-home` to override `DSH_HOME` or `~/.dsh`.
Use `sessionmgr help` for all CLI options.

## Safety

- Codex and DeepSeek Harness homes are read-only inputs.
- Raw sessions remain the source of truth; Markdown and metadata are derived files.
- Existing exports are updated only after identity and content-hash checks.
- Missing source sessions do not delete previous exports.
- Attachments are copied only when they pass size, stability, and integrity checks.
- Review generated Markdown and attachments before publishing them publicly.

## Build

Git and GNU Make are required. The build wrapper uses Go 1.24+ from `PATH`, or
downloads the repository-pinned portable Go toolchain after verifying its checksum.

```bash
make build
make check
make dist
```

On Windows, a release-equivalent pair of icon-bearing executables can be built with:

```powershell
.\scripts\build-windows-release.ps1 -Version 0.6.0
```

Technical details are documented in the [product requirements](docs/PRD.md),
[format specification](docs/SPEC.md), and [v0.6.0 devlog](docs/devlogs/v0.6.0.md).
