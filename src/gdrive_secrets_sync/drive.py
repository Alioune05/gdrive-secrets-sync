from __future__ import annotations

import os
import sys
from pathlib import Path

from google.auth.transport.requests import Request
from google.oauth2.credentials import Credentials
from google_auth_oauthlib.flow import InstalledAppFlow
from googleapiclient.discovery import build
from googleapiclient.http import MediaFileUpload, MediaIoBaseDownload

# Full Drive access, not the narrower drive.file scope: the target folder is
# normally created by hand in the Drive UI rather than by this app, and
# drive.file can only ever see files/folders the app itself created. This is
# your own OAuth client on your own Google Cloud project, so the resulting
# token is only ever held by you.
SCOPES = ["https://www.googleapis.com/auth/drive"]


def get_service(credentials_path: Path, token_path: Path):
    """Run (or refresh) the OAuth flow and return an authenticated Drive client."""
    creds = None
    if token_path.exists():
        creds = Credentials.from_authorized_user_file(str(token_path), SCOPES)

    if not creds or not creds.valid:
        if creds and creds.expired and creds.refresh_token:
            creds.refresh(Request())
        else:
            if not credentials_path.exists():
                print(
                    f"OAuth client secret not found at {credentials_path}.\n"
                    "See README.md for one-time setup.",
                    file=sys.stderr,
                )
                sys.exit(1)
            flow = InstalledAppFlow.from_client_secrets_file(str(credentials_path), SCOPES)
            creds = flow.run_local_server(port=0)

        token_path.parent.mkdir(parents=True, exist_ok=True)
        token_path.write_text(creds.to_json())
        os.chmod(token_path, 0o600)

    return build("drive", "v3", credentials=creds, cache_discovery=False)


def find_folder(service, name: str) -> dict | None:
    query = (
        "mimeType = 'application/vnd.google-apps.folder' "
        f"and name = '{name}' and trashed = false"
    )
    resp = service.files().list(q=query, spaces="drive", fields="files(id, name)").execute()
    files = resp.get("files", [])
    return files[0] if files else None


def require_folder(service, name: str) -> dict:
    folder = find_folder(service, name)
    if not folder:
        print(
            f"Drive folder '{name}' not found in My Drive.\n"
            "Create it (or check the 'drive.folder' value in your config) and retry.",
            file=sys.stderr,
        )
        sys.exit(1)
    return folder


def find_file_in_folder(service, filename: str, folder_id: str) -> dict | None:
    query = f"name = '{filename}' and '{folder_id}' in parents and trashed = false"
    resp = (
        service.files()
        .list(q=query, spaces="drive", fields="files(id, name, modifiedTime, size)")
        .execute()
    )
    files = resp.get("files", [])
    if not files:
        return None
    files.sort(key=lambda f: f.get("modifiedTime", ""), reverse=True)
    return files[0]


def download_file(service, file_id: str, dest: Path) -> None:
    request = service.files().get_media(fileId=file_id)
    with open(dest, "wb") as fh:
        downloader = MediaIoBaseDownload(fh, request)
        done = False
        while not done:
            _, done = downloader.next_chunk()


def upload_or_update(
    service, zip_path: Path, filename: str, folder_id: str, existing: dict | None
) -> tuple[str, str]:
    media = MediaFileUpload(str(zip_path), mimetype="application/zip")
    if existing:
        service.files().update(fileId=existing["id"], media_body=media).execute()
        return existing["id"], "updated"
    metadata = {"name": filename, "parents": [folder_id]}
    created = service.files().create(body=metadata, media_body=media, fields="id").execute()
    return created["id"], "created"
