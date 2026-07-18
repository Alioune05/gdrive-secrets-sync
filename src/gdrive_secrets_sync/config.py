from __future__ import annotations

import sys
from dataclasses import dataclass
from pathlib import Path

import yaml

CONFIG_FILENAMES = (".gdrive-sync.yaml", ".gdrive-sync.yml")

TEMPLATE = """\
# gdrive-secrets-sync config.
# Paths under each group are relative to this file's directory (your repo root).
# Run `gdrive-secrets-sync status` after editing this to sanity-check it.

drive:
  folder: my-project        # Folder name at the root of your Google Drive
  filename: my-secrets.zip  # Zip file name inside that folder

groups:
  example-group:
    - path/to/secret-file.txt
    - another/secret-dir/key.json
"""


@dataclass
class DriveConfig:
    folder: str
    filename: str


@dataclass
class SyncConfig:
    path: Path  # the config file itself
    root: Path  # directory relative paths are resolved against
    drive: DriveConfig
    groups: dict[str, list[str]]

    def resolve_files(self, group_names: list[str]) -> list[str]:
        unknown = [g for g in group_names if g not in self.groups]
        if unknown:
            print(
                f"Unknown group(s): {', '.join(unknown)}. Known groups: {', '.join(self.groups)}",
                file=sys.stderr,
            )
            sys.exit(1)
        seen: list[str] = []
        for group in group_names:
            for rel in self.groups[group]:
                if rel not in seen:
                    seen.append(rel)
        return seen


def find_config(start: Path | None = None) -> Path | None:
    """Walk upward from `start` (default: cwd), like git looks for .git."""
    current = (start or Path.cwd()).resolve()
    for directory in (current, *current.parents):
        for name in CONFIG_FILENAMES:
            candidate = directory / name
            if candidate.is_file():
                return candidate
    return None


def load_config(path: Path) -> SyncConfig:
    try:
        data = yaml.safe_load(path.read_text()) or {}
    except yaml.YAMLError as exc:
        print(f"Failed to parse {path}: {exc}", file=sys.stderr)
        sys.exit(1)

    drive_raw = data.get("drive") or {}
    folder = drive_raw.get("folder")
    filename = drive_raw.get("filename")
    if not folder or not filename:
        print(f"{path}: 'drive.folder' and 'drive.filename' are required.", file=sys.stderr)
        sys.exit(1)

    groups_raw = data.get("groups") or {}
    if not groups_raw:
        print(f"{path}: at least one entry under 'groups' is required.", file=sys.stderr)
        sys.exit(1)

    groups: dict[str, list[str]] = {}
    for name, files in groups_raw.items():
        if not isinstance(files, list) or not files:
            print(f"{path}: group '{name}' must be a non-empty list of file paths.", file=sys.stderr)
            sys.exit(1)
        groups[name] = [str(f) for f in files]

    return SyncConfig(
        path=path,
        root=path.parent,
        drive=DriveConfig(folder=folder, filename=filename),
        groups=groups,
    )


def scaffold(path: Path) -> None:
    if path.exists():
        print(f"{path} already exists, not overwriting.", file=sys.stderr)
        sys.exit(1)
    path.write_text(TEMPLATE)
    print(f"Wrote template config to {path}.")
    print("Edit it, then run 'gdrive-secrets-sync status' to sanity-check it.")
