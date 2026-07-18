// Package domain holds the core types and sentinel errors shared across the
// application, independent of Google Drive, the filesystem, or the CLI. The
// adapters (drive), the archive layer, and the syncer usecase all speak in
// terms of these types so each can be developed and tested in isolation.
package domain

import "errors"

// Sentinel errors returned by the usecase and adapters. Callers use
// errors.Is to branch on them (e.g. the CLI turns them into exit codes and
// friendly messages) rather than matching on strings.
var (
	// ErrConfigNotFound means no .gdrive-sync.yaml could be discovered.
	ErrConfigNotFound = errors.New("config not found")
	// ErrInvalidConfig means a config file was found but failed validation.
	ErrInvalidConfig = errors.New("invalid config")
	// ErrUnknownGroup means a requested group is absent from the config.
	ErrUnknownGroup = errors.New("unknown group")
	// ErrFolderNotFound means the target Drive folder does not exist.
	ErrFolderNotFound = errors.New("drive folder not found")
	// ErrRemoteNotFound means the target file is absent from the Drive folder.
	ErrRemoteNotFound = errors.New("remote file not found")
	// ErrNothingToUpload means none of the selected files exist locally.
	ErrNothingToUpload = errors.New("nothing to upload")
)

// RemoteFile is the subset of Drive file metadata the tool cares about. It is
// deliberately string-typed to mirror what the Drive API returns (size and
// modifiedTime arrive as strings), keeping the adapter a thin translation.
type RemoteFile struct {
	ID           string
	Name         string
	ModifiedTime string
	Size         string
}

// UploadAction describes what an upload did to the remote file.
type UploadAction string

const (
	// ActionCreated means a new file was created in the folder.
	ActionCreated UploadAction = "created"
	// ActionUpdated means an existing file was updated in place.
	ActionUpdated UploadAction = "updated"
)
