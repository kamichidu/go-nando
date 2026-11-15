# Versions and update policy

`v1.0.0` starts compatibility guarantees. Compatibility with repository, configuration, CLI, schema, or Recovery state created before `v1.0.0` is not guaranteed.

| SemVer component | Policy |
| --- | --- |
| Patch | Backward-compatible fixes |
| Minor | Backward-compatible features |
| Major | Breaking changes may be introduced |

This policy does not update binaries automatically. An operator chooses when to install a release and restart a running daemon.

## Release versions and SQLite schema versions

The application SemVer release and SQLite `PRAGMA user_version` are separate version axes. `user_version` records only the SQLite schema revision; it does not identify which application release last opened the repository. A Minor release that does not change the schema leaves `user_version` unchanged. Each SQLite schema change increments it by exactly one.

## Migration boundary

A migration is available only through the explicit `nando admin migrate` command. Startup and `init` do not migrate existing repositories. For a schema-changing Minor release, the supported migration source is the persisted Repository Format and metadata schema state emitted by the immediately preceding SemVer Minor line, whether or not that line changed the schema.

For example, if `1.3.x` makes no schema change after `1.2.x` created schema revision 4, then `1.4.0` migrates from revision 4 to revision 5. The release number is not stored in `user_version`. If instead `1.3.0` changed revision 4 to 5, then `1.4.0` expects revision 5 as its source; it rejects revision 4. Run the `1.3.x` migration before advancing to `1.4.0`.

The migration command rejects persisted Repository Format or schema state that does not match the declared source state. It cannot cross multiple persisted transitions or downgrade a repository. If a schema-free Minor line and an older line emitted identical Repository Format and schema state, the metadata cannot distinguish their origins. Because no application-release marker is stored, a direct upgrade from that older line cannot be rejected solely by its release number. The policy adds no release marker and does not treat `user_version` as a SemVer version.

The current v1 baseline uses schema revision 1. General Repository Format and metadata schema migrations are not implemented yet; `nando admin migrate` currently validates schema revision 1 only. Unsupported persisted state is rejected without being rewritten.
