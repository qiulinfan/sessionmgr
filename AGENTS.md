# Session Manager Development Rules

These rules apply to the entire repository. Every human or coding agent must
follow them before committing or publishing changes.

## 1. A devlog is mandatory for every version

- Every product version must have a matching file at
  `docs/devlogs/v<version>.md`.
- The devlog must be created or updated in the same change that introduces
  code, behavior, schema, dependency, or documentation changes for that
  version.
- A version change is not complete and must not be tagged or pushed for release
  until its devlog is current.
- While a version is under development, append material work to its existing
  devlog instead of creating ad-hoc status files.
- After a version is released or tagged, treat its devlog as immutable. Record
  corrections in an explicitly dated addendum or in the next version's devlog.
- Keep `docs/devlogs/README.md` updated whenever a version devlog is added.

Every devlog must contain:

1. version, status, and date;
2. objective and scope;
3. work completed, grouped by product area;
4. important architecture or product decisions and their rationale;
5. validation actually performed, including exact commands and results;
6. known limitations, risks, and unverified assumptions;
7. migration or compatibility notes when formats or behavior changed;
8. concrete next steps for the next developer or agent;
9. a concise list of the most relevant files or modules.

Use `docs/devlogs/TEMPLATE.md` when starting a new version.

## 2. Preserve Session Manager's safety invariants

- Never silently overwrite a Run, ref, restored user file, native Agent
  session, or conflicting catalog identity.
- Raw Agent session data is the archival source of truth. Normalized events and
  handoffs are derived artifacts and must not replace raw data.
- Never archive or print authentication databases, complete secrets,
  credential-helper output, or environment-variable values.
- Publish immutable objects before publishing their Run ref.
- Verify required objects and checksums before restore or remote publication.
- Keep native resume capability labels honest: experimental and degraded paths
  must not be presented as stable success.

## 3. Testing and evidence

- Run `go test ./...` and `go vet ./...` for every implementation change.
- Run `go test -race ./...` for changes involving stores, catalog access,
  synchronization, atomic publication, or concurrent behavior.
- Run the end-to-end capture/push/pull/restore test when changing Git,
  Capsule, Store, adapter, or restore behavior.
- For portability changes, build with `CGO_ENABLED=0` for both supported
  platform families when possible.
- Do not claim a test, remote integration, migration, or native resume path was
  verified unless it was actually executed. Record anything unverified in the
  current devlog.

## 4. Compatibility and formats

- Version persisted schemas and normalized parsers explicitly.
- Unknown optional data should remain inspectable; unknown required
  capabilities must block restore.
- Preserve backward compatibility within a schema major version.
- Any manifest, event, CLI JSON, encryption, or Store layout change must include
  compatibility notes and relevant fixture or round-trip tests.

## 5. Repository hygiene

- Keep source, tests, schemas, documentation, and the current devlog in the
  same reviewable change.
- Do not commit build artifacts, local Session Manager homes, real sessions,
  credentials, private keys, or user data.
- Preserve unrelated user changes in a dirty worktree.
- Prefer focused commits with messages that describe the product outcome.
- Before push, inspect the staged diff and confirm the current devlog accurately
  describes it.
