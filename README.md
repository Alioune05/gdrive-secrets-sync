# gdrive-secrets-sync

A general-purpose CLI to sync git-ignored local secret files (API keys,
Terraform state, service-account JSONs, etc.) with a single zip stored in a
Google Drive folder — using your own Google account via OAuth, and a small
per-repo YAML config that describes what to sync and where.

Not tied to any one project: install it once, then drop a `.gdrive-sync.yaml`
in any repo that needs this.

## Why

Some machines can't keep certain files on disk long-term (secret scanners
flag them), but the files are still needed locally from time to time (running
Terraform, building an app, running a service locally). This tool zips them
up, and syncs that zip to/from a folder in your Google Drive.

## Install

Requires [Go](https://go.dev/) 1.26+.

```bash
cd ~/gdrive-secrets-sync
go install ./cmd/gdrive-secrets-sync
```

This builds and places a `gdrive-secrets-sync` binary in `$(go env GOPATH)/bin`
(usually `~/go/bin`) — make sure that directory is on your `PATH`. Re-run the
same command any time you pull updates to this repo, to rebuild.

## One-time OAuth setup

1. Go to https://console.cloud.google.com/ and create a project (or reuse a
   personal one).
2. **APIs & Services → Library**: enable the **Google Drive API**.
3. **APIs & Services / Google Auth Platform → OAuth consent screen** (a.k.a.
   "Audience"/"Branding" in the newer UI): choose **External**, fill in an
   app name + your email, and add yourself as a **test user**. Test mode
   works indefinitely for your own account — no verification needed.
4. **Credentials / Clients → Create Credentials → OAuth client ID**:
   - Application type: **Desktop app**
   - Give it any name, e.g. `gdrive-secrets-sync-cli`
5. Download the client secret JSON (or copy the Client ID + Client secret
   shown on the client's detail page if there's no direct download button).

Then install it locally, outside any repo:

```bash
mkdir -p ~/.config/gdrive-secrets-sync
mv ~/Downloads/client_secret_*.json ~/.config/gdrive-secrets-sync/credentials.json
chmod 600 ~/.config/gdrive-secrets-sync/credentials.json
```

The tool requests the **full Drive scope**
(`https://www.googleapis.com/auth/drive`), not the narrower `drive.file`,
because it needs to see Drive folders you created by hand in the Drive UI —
`drive.file` can only ever see files/folders the app itself created. This is
your own OAuth client on your own Google Cloud project, so the resulting
token is only ever held by you.

The first `pull`/`push`/`status` opens a browser window once to grant
access; after that, a token is cached at
`~/.config/gdrive-secrets-sync/token.json` and you won't be prompted again.

## Per-repo config

In any repo that needs this, create a `.gdrive-sync.yaml` at the repo root
(or anywhere — the tool searches upward from your current directory, the way
git looks for `.git`):

```bash
cd /path/to/your/repo
gdrive-secrets-sync init
```

This scaffolds:

```yaml
drive:
  folder: my-project        # Folder name at the root of your Google Drive
  filename: my-secrets.zip  # Zip file name inside that folder

groups:
  example-group:
    - path/to/secret-file.txt
    - another/secret-dir/key.json
```

Edit it: rename `drive.folder`/`drive.filename` to match the Drive folder
you actually use, and list one or more named `groups`, each a list of file
paths **relative to the config file's own directory**. Group names and file
lists are entirely up to you — use as many groups as you like.

All of a repo's groups zip into **one** file in Drive (`drive.filename`
inside `drive.folder`). `--groups` on any command lets you restrict to a
subset instead of syncing everything.

## Usage

```bash
# Pull everything described by the config back to disk:
gdrive-secrets-sync pull

# Only a subset of groups:
gdrive-secrets-sync pull --groups terraform android

# Push local files up (creates the Drive zip if it doesn't exist yet,
# otherwise updates it in place):
gdrive-secrets-sync push

# Push, then delete the local files that were just uploaded (prompts first):
gdrive-secrets-sync push --delete

# Same, without the confirmation prompt:
gdrive-secrets-sync -y push --delete

# See what's present locally vs. in the Drive zip, without downloading/uploading:
gdrive-secrets-sync status

# Point explicitly at a config instead of relying on auto-discovery:
gdrive-secrets-sync --config /path/to/.gdrive-sync.yaml status

# Print help (any of -h / --h / --help, before or after a command):
gdrive-secrets-sync -h
gdrive-secrets-sync pull --h
```

`pull` prompts before overwriting a file that already exists locally, unless
you pass `-y`/`--yes`. `push --delete` likewise prompts before deleting the
local files it just uploaded, unless `-y`/`--yes` is passed.

If a zip in Drive was created before you added structure (e.g. a flat zip
with no subfolders), `pull` still works: it matches each zip entry to a
config path first by exact relative path, then falls back to matching by
filename.

## Flags / env vars

| Flag              | Env var                              | Default                                          |
|-------------------|----------------------------------------|---------------------------------------------------|
| `--config`        | —                                       | auto-discovered `.gdrive-sync.yaml`               |
| `--credentials`   | `GDRIVE_SECRETS_SYNC_CREDENTIALS`      | `~/.config/gdrive-secrets-sync/credentials.json`  |
| `--token`         | `GDRIVE_SECRETS_SYNC_TOKEN`            | `~/.config/gdrive-secrets-sync/token.json`        |
| `--groups`        | —                                       | all groups in the config                          |
| `-y`, `--yes`     | —                                       | prompt before overwriting/deleting                |
| `-h`, `--h`, `--help` | —                                   | print help and exit                               |

## Architecture

The code follows a small hexagonal layout inspired by the larger Go services
in this account, so each concern is isolated and independently testable:

```
cmd/gdrive-secrets-sync   Thin entrypoint: hands os.Args + streams to the CLI.
internal/domain           Core types (RemoteFile, UploadAction) + sentinel errors.
internal/config           Loads/validates/scaffolds .gdrive-sync.yaml (filesystem only).
internal/archive          Zip create/extract; overwrite decisions are injected, no console I/O.
internal/drive            Google Drive adapter (OAuth + Drive v3); the only package that imports the Google SDK.
internal/syncer           Usecase: orchestrates pull/push/status over the RemoteStore and Archiver ports.
internal/cli              Argument parsing + wiring of the concrete adapters into the usecase.
```

The `syncer` usecase depends on the `RemoteStore` and `Archiver` interfaces
rather than concrete implementations, so its full pull/push/status logic is
exercised in tests with in-memory fakes — no network or real Google account
required.

## Development

```bash
# Run the whole test suite with the race detector and coverage:
go test ./... -race -cover

# Vet + build:
go vet ./...
go build ./...
```

CI (GitHub Actions, `.github/workflows/ci.yml`) runs gofmt, `go vet`,
`go build`, and the race-enabled test suite with a coverage profile on every
push to `main` and every pull request.

## Safety notes

- Never prints file contents — only file names/paths/sizes/Drive IDs.
- Make sure every file/group you list in `.gdrive-sync.yaml` is also covered
  by that repo's `.gitignore`, so a `pull` can never end up committed.
- `credentials.json`/`token.json` live outside any repo, under
  `~/.config/gdrive-secrets-sync/`, so they're never at risk of being
  committed either.
