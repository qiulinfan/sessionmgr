# Session archive

Run `./scripts/export-codex-sessions` from the repository root to populate this
directory with repository/device/session trees.

Each device/session has one current `conversation.md`; Git history retains older
revisions. Generated Markdown, attachments, and hidden identity sidecars can be
committed and merged with ordinary Git. No Session Manager-specific database or
remote service is required.

Hosted Git sessions are included by default. Use `--include-non-git` when a
deliberate full export of sessions from non-Git or local-only directories is
also wanted.
