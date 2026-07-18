from __future__ import annotations

import argparse
import os
import shutil
import sys
import tempfile
import zipfile
from datetime import datetime
from pathlib import Path, PurePosixPath

from . import drive
from .config import SyncConfig, find_config, load_config, scaffold

DEFAULT_CONFIG_DIR = Path.home() / ".config" / "gdrive-secrets-sync"
DEFAULT_CREDENTIALS = DEFAULT_CONFIG_DIR / "credentials.json"
DEFAULT_TOKEN = DEFAULT_CONFIG_DIR / "token.json"


def _load(args: argparse.Namespace) -> SyncConfig:
    config_path = args.config or find_config()
    if not config_path:
        print(
            "No .gdrive-sync.yaml found in this directory or any parent.\n"
            "Run 'gdrive-secrets-sync init' to create one, or pass --config.",
            file=sys.stderr,
        )
        sys.exit(1)
    return load_config(config_path)


def cmd_init(args: argparse.Namespace) -> None:
    target = args.path or (Path.cwd() / ".gdrive-sync.yaml")
    scaffold(target)


def cmd_pull(args: argparse.Namespace) -> None:
    cfg = _load(args)
    groups = args.groups or list(cfg.groups)
    wanted = set(cfg.resolve_files(groups))
    wanted_basenames = {Path(rel).name: rel for rel in wanted}

    service = drive.get_service(args.credentials, args.token)
    folder = drive.require_folder(service, cfg.drive.folder)
    remote = drive.find_file_in_folder(service, cfg.drive.filename, folder["id"])
    if not remote:
        print(
            f"No file named '{cfg.drive.filename}' found in Drive folder '{cfg.drive.folder}'.",
            file=sys.stderr,
        )
        sys.exit(1)

    print(
        f"Found '{remote['name']}' in Drive folder '{cfg.drive.folder}' "
        f"(id={remote['id']}, size={remote.get('size', '?')} bytes)."
    )

    with tempfile.TemporaryDirectory() as tmp:
        zip_path = Path(tmp) / cfg.drive.filename
        drive.download_file(service, remote["id"], zip_path)

        with zipfile.ZipFile(zip_path) as zf:
            names = zf.namelist()
            print("Zip contains:", ", ".join(names) or "(empty)")

            for name in names:
                posix_name = PurePosixPath(name.replace("\\", "/"))
                # Prefer matching the exact relative path from the config;
                # fall back to matching by basename so a zip created before
                # groups existed (flat, no directories) still lands right.
                if str(posix_name) in wanted:
                    rel_dest = str(posix_name)
                elif posix_name.name in wanted_basenames:
                    rel_dest = wanted_basenames[posix_name.name]
                else:
                    continue

                dest = cfg.root / rel_dest
                if dest.exists() and not args.yes:
                    answer = input(f"{dest} already exists locally. Overwrite? [y/N] ")
                    if answer.strip().lower() != "y":
                        print(f"Skipping {dest}")
                        continue

                dest.parent.mkdir(parents=True, exist_ok=True)
                with zf.open(name) as src, open(dest, "wb") as out:
                    shutil.copyfileobj(src, out)
                os.chmod(dest, 0o600)
                print(f"Wrote {dest}")

    print("Pull complete.")


def cmd_push(args: argparse.Namespace) -> None:
    cfg = _load(args)
    groups = args.groups or list(cfg.groups)
    wanted = cfg.resolve_files(groups)
    existing = [rel for rel in wanted if (cfg.root / rel).exists()]
    missing = [rel for rel in wanted if rel not in existing]
    if missing:
        print(f"Note: not found locally, skipping: {', '.join(missing)}")
    if not existing:
        print("Nothing to upload: none of the target files exist locally.", file=sys.stderr)
        sys.exit(1)

    service = drive.get_service(args.credentials, args.token)
    folder = drive.require_folder(service, cfg.drive.folder)

    with tempfile.TemporaryDirectory() as tmp:
        zip_path = Path(tmp) / cfg.drive.filename
        with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
            for rel in existing:
                zf.write(cfg.root / rel, arcname=rel)
                print(f"Adding {rel} to archive")

        remote = drive.find_file_in_folder(service, cfg.drive.filename, folder["id"])
        file_id, action = drive.upload_or_update(
            service, zip_path, cfg.drive.filename, folder["id"], remote
        )

    verb = "Updated existing" if action == "updated" else "Uploaded new"
    print(
        f"{verb} '{cfg.drive.filename}' in Drive folder '{cfg.drive.folder}' "
        f"(id={file_id}) — {datetime.now().isoformat(timespec='seconds')}"
    )
    print("Push complete.")

    if args.delete:
        if not args.yes:
            answer = input(
                f"Delete {len(existing)} local file(s) that were just pushed? [y/N] "
            )
            if answer.strip().lower() != "y":
                print("Not deleting local files.")
                return
        for rel in existing:
            (cfg.root / rel).unlink()
            print(f"Deleted {cfg.root / rel}")
        print("Local cleanup complete.")


def cmd_status(args: argparse.Namespace) -> None:
    cfg = _load(args)
    groups = args.groups or list(cfg.groups)
    wanted = cfg.resolve_files(groups)

    print(f"Config: {cfg.path}")
    print("Local files:")
    for rel in wanted:
        p = cfg.root / rel
        print(f"  {rel}: {'present' if p.exists() else 'MISSING'}")

    service = drive.get_service(args.credentials, args.token)
    folder = drive.require_folder(service, cfg.drive.folder)
    remote = drive.find_file_in_folder(service, cfg.drive.filename, folder["id"])
    if remote:
        print(
            f"Drive ('{cfg.drive.folder}'): '{remote['name']}' present "
            f"(id={remote['id']}, modified={remote.get('modifiedTime')}, "
            f"size={remote.get('size', '?')} bytes)"
        )
    else:
        print(f"Drive ('{cfg.drive.folder}'): '{cfg.drive.filename}' NOT found")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="gdrive-secrets-sync",
        description=(
            "Sync git-ignored local secret files with a zip stored in a Google "
            "Drive folder, driven by a per-repo .gdrive-sync.yaml config."
        ),
    )
    parser.add_argument(
        "--config",
        type=Path,
        default=None,
        help=(
            "Path to the config file (default: auto-discovered .gdrive-sync.yaml, "
            "searching this directory and its parents, like git looks for .git)"
        ),
    )
    parser.add_argument(
        "--credentials",
        type=Path,
        default=Path(os.environ.get("GDRIVE_SECRETS_SYNC_CREDENTIALS", DEFAULT_CREDENTIALS)),
        help=f"Path to OAuth client secret JSON (default: {DEFAULT_CREDENTIALS})",
    )
    parser.add_argument(
        "--token",
        type=Path,
        default=Path(os.environ.get("GDRIVE_SECRETS_SYNC_TOKEN", DEFAULT_TOKEN)),
        help=f"Path to cached OAuth token (default: {DEFAULT_TOKEN})",
    )
    parser.add_argument(
        "--groups",
        nargs="+",
        default=None,
        help="Which groups (from the config) to sync (default: all groups in the config)",
    )
    parser.add_argument(
        "-y",
        "--yes",
        action="store_true",
        help="Overwrite local files without prompting (pull only)",
    )

    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("pull", help="Download the zip from Drive and extract the selected groups")
    push_parser = sub.add_parser(
        "push", help="Zip the selected groups and upload/update the zip on Drive"
    )
    push_parser.add_argument(
        "--delete",
        action="store_true",
        help="After a successful push, delete the local files that were uploaded "
        "(prompts for confirmation unless -y/--yes is also passed)",
    )
    sub.add_parser("status", help="Show what's present locally vs. on Drive")
    init_parser = sub.add_parser(
        "init", help="Scaffold a starter .gdrive-sync.yaml in the current directory"
    )
    init_parser.add_argument(
        "path",
        nargs="?",
        type=Path,
        default=None,
        help="Where to write the config (default: ./.gdrive-sync.yaml)",
    )

    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()

    if args.command == "init":
        cmd_init(args)
    elif args.command == "pull":
        cmd_pull(args)
    elif args.command == "push":
        cmd_push(args)
    elif args.command == "status":
        cmd_status(args)


if __name__ == "__main__":
    main()
