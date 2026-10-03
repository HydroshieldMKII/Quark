# Proton Drive CLI contract (as used by Quark)

**Verified against the real CLI on 2026-10-03** (`testdata/cli/0.8.0/`, 39 recorded steps, all
as expected; `fixtures_test.go` replays them). Additions seen in real output: `urlAccess.creatorEmail`,
device `hasDeprecatedName`. Measured: listing a trash of ~4,900 items took 1–2 minutes, produced
4.7 MB of JSON and used ~360 MB of memory in one CLI process.

Source of truth: the CLI's own source code, MIT-licensed, at
`https://github.com/ProtonDriveApps/sdk` tag `js/v0.21.1` (`cli/` = cli-drive 0.8.0, SDK js 0.21.0),
read on 2026-10-03. Recorded fixtures under `testdata/cli/0.8.0/` confirm or override this file.
When the CLI version changes, re-read the source diff and re-capture fixtures.

## Process

| Aspect | Behaviour |
|---|---|
| Invocation | `proton-drive <group> <command> [options] args...`. Group `fs` = `filesystem`; prefixes are matched. **Quark always uses full names.** |
| Positionals | Quark always puts `--` before positionals so names starting with `-` are safe (verified). `--long=value` works. |
| Options | Must come **after** `<group> <command>` (argv[2] and argv[3] are the group and command; `proton-drive --json fs list /` fails as "command not found"). Node `util.parseArgs`, strict. Global per command: `-h/--help`, `-j/--json`, `-v/--verbose`. Options may appear anywhere. Use `--` before positionals that may start with `-`. |
| No args | Starts a REPL. Quark never does this. |
| `--version` | Prints two lines, then fetches `https://proton.me/download/drive/cli/version.json` (5 s timeout) and prints an "latest"/"newer version" line. Exit 0. |
| Success | Exit 0. |
| Usage error (`CommandNotFound`, `InvalidCommandArguments`, parseArgs `TypeError`) | Usage text on **stdout**, message on stderr, exit 1. |
| Recoverable error (`ProtonDriveError`, `CommandError`, `AccountApiError`) | `error.message` as one line on **stderr**, exit 1. `ValidationError` is a `ProtonDriveError`. |
| Fatal error | `=====...` line, stack trace and `Error details:` dump on stderr, exit 1. |
| `ExitError` | Exit 2. |
| Not logged in | stderr `You need to login first`, exit 1. |
| Logs | `-v` adds coloured log lines on **stdout** (verified 2026-10-03), which corrupts JSON: **never pass `-v`**. File log in `$XDG_STATE_HOME/proton-drive-cli/` at `PROTON_DRIVE_LOG_LEVEL` (default DEBUG). |
| Telemetry | Official builds send crash reports to Proton's Sentry and metrics (`Telemetry enabled: build=true, user=false`). |

## JSON output shapes

* **Single object**: `JSON.stringify(obj)` on one line. Dates are ISO-8601 strings.
  `printObject(undefined)` prints the literal text `undefined` (not JSON).
* **Iterables**: streamed array: `[\n` + items joined by `,\n` + `\n]\n`. An empty list is `[\n\n]\n`.
  If the command fails mid-stream, stdout holds a **truncated array** and stderr the error.
* **Batch results** (`copy`, `move`, `trash`, `restore`, `delete`): array of
  `{"uid": "...", "ok": true}` or `{"uid": "...", "ok": false, "error": {}}`.
  The error is a JS `Error`, which serialises to `{}`: **the failure reason is lost in JSON mode**.
  `copy` success items also carry `newUid`.
* **Transfer summary** (`upload`, `download`): one line
  `{"transferredItems":N,"transferredBytes":N,"skippedItems":N,"failedItems":N,"failures":[{"name":"...","nodeUid":"...","error":"Name: message"}]}`.
  If `failedItems > 0`, stderr also gets `N item(s) failed to upload|download` and exit is 1.
  **No progress output in JSON mode** (progress is drawn only when stdout is a TTY).

### NodeEntity (`fs list` items, `fs info`, `create-folder`, `rename`)

```
uid, parentUid?, name: Result<string, Error|{name,error}>, keyAuthor: Author, nameAuthor: Author,
directRole: viewer|editor|admin|inherited, membership?: {role, inviteTime, sharedBy: Author},
ownedBy: {email?, organization?}, type: file|folder|album|photo, mediaType?, isShared, isSharedByUrl,
deprecatedShareId?, creationTime, modificationTime, trashTime?, totalStorageSize?,
activeRevision?: {uid, state: active|superseded, creationTime, contentAuthor: Author, storageSize,
  isImported, claimedSize?, claimedModificationTime?, claimedDigests?: {sha1?, sha1Verified},
  claimedAdditionalMetadata?},
folder?: {claimedModificationTime?, isImported}, treeEventScopeId, errors?: unknown[]
Result<T,E> = {"ok":true,"value":T} | {"ok":false,"error":E}
Author = Result<string|null, {claimedAuthor?: string|null, error: string}>
```

Display name rule (CLI `getName`): `name.value` if ok and non-empty, else the node `uid`.
Name error forms: `{}` (Error) or `{name: placeholder, error: string}` (InvalidNameError).

### Other shapes

* `fs list /` → `[{"path":"/my-files"}, ... ]` for `/my-files /devices /shared-by-me /shared-with-me /trash /albums /photos /photos-shared-by-me /photos-shared-with-me /photos-trash`.
* `fs list /devices` → Device items: `{uid, type, name: Result, rootFolderUid, creationTime, lastSyncTime?, shareId}`.
* `sharing status|invite|set-url|remove|remove-url` → ShareResult or `undefined` when not shared:
  `{protonInvitations: Member[], nonProtonInvitations: (Member & {state: pending|userRegistered})[], members: Member[], urlAccess?: {uid, creationTime, role, url, customPassword?, expirationTime?, numberOfInitializedDownloads}, editorsCanShare}`
  with `Member = {uid, invitationTime, addedByEmail: Result<string,...>, inviteeEmail, role}`.
* `invitation list` → items `Member & {node: {uid, name: Result, type, mediaType?}}` with `uid` rewritten to `drive~<uid>` or `photos~<uid>`.
* `auth login --json` → one line `{"signInUrl":"https://account.proton.me/..."}`, then polls every 5 s; nothing more on success; exit 0. The CLI itself runs `xdg-open <url>`.
* `auth logout`, `fs empty-trash`, `sharing leave` → no stdout in JSON mode.

## Paths

* POSIX, segments split on unescaped `/`; `\/` is a literal slash. Backslashes are not otherwise
  escaped, so a name ending in `\` cannot be addressed by name.
* Section detection is by **string prefix** (`/my-files…`, `/devices…`, `/shared-with-me…`, `/trash…`, …).
* Each segment resolves to the **first** child whose display name matches. Duplicate names are
  ambiguous.
* A segment matching the node-UID pattern
  `^([A-Za-z0-9=_-]{88,108}|[A-Za-z0-9_-]{22})~([A-Za-z0-9=_-]{88,108}|[A-Za-z0-9_-]{22})$`
  resolves **directly to that node, wherever it is**. Quark addresses existing nodes as
  `/my-files/<uid>` (exact, collision-free). The test sandbox guard rejects UID segments.
* `/shared-with-me/<root-name>/…`: the first segment is matched by name among shared roots.
* `/trash/<name>`: single segment only, matched by **name only** against trashed nodes (first
  match). Restore/delete of duplicate names in trash is ambiguous; Quark refuses it.
* `/devices/<device-name>/…`: device matched by name.

## Commands Quark uses

| Command | Args | Notes |
|---|---|---|
| `filesystem list [-t TYPE] PATH` | | Streamed array of NodeEntity (or roots/devices). `/shared-by-me`, `/trash` only at section root. |
| `filesystem info PATH` | | NodeEntity. |
| `filesystem create-folder PARENT NAME` | | Parent in my-files, devices or shared-with-me. Returns NodeEntity. |
| `filesystem rename PATH NEWNAME` | | Returns NodeEntity. |
| `filesystem copy [-n NAME] SRC... TARGET` | | Batch results with `newUid`. `-n` only with one source. |
| `filesystem move SRC... TARGET` | | Batch results. Same owner only. |
| `filesystem trash PATH...` | | Batch results. All paths must be in one section kind. |
| `filesystem restore /trash/NAME...` | | Batch results. |
| `filesystem delete /trash/NAME...` | | Batch results. Permanent. |
| `filesystem empty-trash` | | Asynchronous on the server. Permanent. |
| `filesystem upload -f S -d S [-t] LOCAL... PARENT` | | `-f`: create-new-revision, rename, replace, skip. `-d`: merge, rename, replace, skip. Identical content (SHA-1) is skipped automatically. Without a strategy in JSON mode a conflict becomes a failure `Name conflict on "x" (file) already exists`. |
| `filesystem download -f S -d S PATH... LOCALDIR` | | `-f`: rename, remove, skip. `-d`: merge, rename, remove, skip. `remove` **deletes local files**. Docs/Sheets skipped. |
| `sharing status PATH` | | ShareResult or `undefined`. |
| `sharing invite -u EMAIL... -r ROLE [-m MSG] [-n] PATH` | | Sends emails. Role viewer, editor, admin. |
| `sharing remove -e EMAIL... \| -a PATH` | | `-a` removes everyone. |
| `sharing set-url [--role R] [--password P] [--expiration YYYY-MM-DD] PATH` | | Role viewer or editor. |
| `sharing remove-url PATH`, `sharing leave PATH` | | |
| `invitation list`, `invitation accept UID`, `invitation reject UID` | | UID is `drive~…` or `photos~…`. |
| `auth login`, `auth logout` | | |

## Environment

`PROTON_DRIVE_CREDENTIALS_STORE` (keychain | pass | unsafe_file), `PROTON_DRIVE_LOG_LEVEL`
(DEBUG | INFO | WARNING | ERROR), `PROTON_DRIVE_CACHE_DIR` (single dir for cache, data and logs),
`PROTON_DRIVE_BASE_URL`, `PROTON_DRIVE_UNSAFE_CACHE`, `NO_COLOR`, `XDG_CACHE_HOME`,
`XDG_DATA_HOME`, `XDG_STATE_HOME`.
