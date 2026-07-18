// Package syncer is the usecase layer: it orchestrates the pull, push, and
// status flows by combining a RemoteStore (Google Drive), an Archiver (zip),
// and the local filesystem. It knows nothing about the concrete Drive SDK or
// the CLI — those are injected as ports — which is what makes every branch
// here unit-testable with fakes.
package syncer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aamoussou/gdrive-secrets-sync/internal/archive"
	"github.com/aamoussou/gdrive-secrets-sync/internal/config"
	"github.com/aamoussou/gdrive-secrets-sync/internal/domain"
)

// RemoteStore is the Google Drive contract the usecase depends on. The drive
// package's *Client satisfies it; tests supply a fake.
type RemoteStore interface {
	FindFolder(ctx context.Context, name string) (*domain.RemoteFile, error)
	FindFile(ctx context.Context, filename, folderID string) (*domain.RemoteFile, error)
	Download(ctx context.Context, fileID, destPath string) error
	Upload(ctx context.Context, localPath, filename, folderID string, existing *domain.RemoteFile) (string, domain.UploadAction, error)
}

// Archiver is the zip contract the usecase depends on. archive.Zip satisfies
// it; tests supply a fake.
type Archiver interface {
	Create(zipPath, root string, relPaths []string) ([]string, error)
	Extract(req archive.ExtractRequest) (archive.ExtractOutcome, error)
}

// Confirm asks the user a yes/no question and reports whether they agreed. It
// is only consulted for destructive actions when the caller has not passed
// -y/--yes.
type Confirm func(prompt string) bool

// Syncer wires the ports together and writes human-readable progress to out.
type Syncer struct {
	store    RemoteStore
	archiver Archiver
	out      io.Writer
	confirm  Confirm
}

// New builds a Syncer. A nil confirm defaults to always-yes, and a nil out
// defaults to discarding output.
func New(store RemoteStore, archiver Archiver, out io.Writer, confirm Confirm) *Syncer {
	if out == nil {
		out = io.Discard
	}
	if confirm == nil {
		confirm = func(string) bool { return true }
	}
	return &Syncer{store: store, archiver: archiver, out: out, confirm: confirm}
}

func (s *Syncer) printf(format string, args ...any) { fmt.Fprintf(s.out, format, args...) }
func (s *Syncer) println(args ...any)               { fmt.Fprintln(s.out, args...) }

// requireFolder resolves the configured Drive folder, turning a missing
// folder into domain.ErrFolderNotFound with a helpful message.
func (s *Syncer) requireFolder(ctx context.Context, cfg *config.Config) (*domain.RemoteFile, error) {
	folder, err := s.store.FindFolder(ctx, cfg.Drive.Folder)
	if err != nil {
		return nil, err
	}
	if folder == nil {
		return nil, fmt.Errorf(
			"%w: '%s' not found in My Drive.\nCreate it (or check the 'drive.folder' value in your config) and retry",
			domain.ErrFolderNotFound, cfg.Drive.Folder)
	}
	return folder, nil
}

// Status reports what is present locally versus on Drive for the selected
// groups (all groups when groups is empty).
func (s *Syncer) Status(ctx context.Context, cfg *config.Config, groups []string) error {
	wanted, err := cfg.ResolveFiles(groups)
	if err != nil {
		return err
	}

	s.printf("Config: %s\n", cfg.Path)
	s.println("Local files:")
	for _, rel := range wanted {
		state := "MISSING"
		if _, err := os.Stat(filepath.Join(cfg.Root, rel)); err == nil {
			state = "present"
		}
		s.printf("  %s: %s\n", rel, state)
	}

	folder, err := s.requireFolder(ctx, cfg)
	if err != nil {
		return err
	}
	remote, err := s.store.FindFile(ctx, cfg.Drive.Filename, folder.ID)
	if err != nil {
		return err
	}
	if remote != nil {
		s.printf("Drive ('%s'): '%s' present (id=%s, modified=%s, size=%s bytes)\n",
			cfg.Drive.Folder, remote.Name, remote.ID, remote.ModifiedTime, remote.Size)
	} else {
		s.printf("Drive ('%s'): '%s' NOT found\n", cfg.Drive.Folder, cfg.Drive.Filename)
	}
	return nil
}

// Pull downloads the Drive zip and extracts the selected groups under the
// config root. When yes is false, it prompts before overwriting existing
// local files.
func (s *Syncer) Pull(ctx context.Context, cfg *config.Config, groups []string, yes bool) error {
	wantedList, err := cfg.ResolveFiles(groups)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	wantedByBase := map[string]string{}
	for _, rel := range wantedList {
		wanted[rel] = true
		wantedByBase[filepath.Base(rel)] = rel
	}

	folder, err := s.requireFolder(ctx, cfg)
	if err != nil {
		return err
	}
	remote, err := s.store.FindFile(ctx, cfg.Drive.Filename, folder.ID)
	if err != nil {
		return err
	}
	if remote == nil {
		return fmt.Errorf("%w: no file named '%s' in Drive folder '%s'",
			domain.ErrRemoteNotFound, cfg.Drive.Filename, cfg.Drive.Folder)
	}

	s.printf("Found '%s' in Drive folder '%s' (id=%s, size=%s bytes).\n",
		remote.Name, cfg.Drive.Folder, remote.ID, remote.Size)

	tmpDir, err := os.MkdirTemp("", "gdrive-secrets-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, cfg.Drive.Filename)
	if err := s.store.Download(ctx, remote.ID, zipPath); err != nil {
		return err
	}

	var overwrite func(string) bool
	if !yes {
		overwrite = func(dest string) bool {
			return s.confirm(fmt.Sprintf("%s already exists locally. Overwrite? [y/N] ", dest))
		}
	}

	outcome, err := s.archiver.Extract(archive.ExtractRequest{
		ZipPath:      zipPath,
		DestRoot:     cfg.Root,
		Wanted:       wanted,
		WantedByBase: wantedByBase,
		Overwrite:    overwrite,
	})
	if err != nil {
		return err
	}

	if len(outcome.AllEntries) == 0 {
		s.println("Zip contains: (empty)")
	} else {
		s.printf("Zip contains: %s\n", strings.Join(outcome.AllEntries, ", "))
	}
	for _, r := range outcome.Results {
		switch r.Status {
		case archive.StatusWritten:
			s.printf("Wrote %s\n", r.Dest)
		case archive.StatusSkipped:
			s.printf("Skipping %s\n", r.Dest)
		}
	}

	s.println("Pull complete.")
	return nil
}

// PushOptions tunes the push flow.
type PushOptions struct {
	// Delete removes the local files that were uploaded after a successful
	// push (prompting first unless Yes is set).
	Delete bool
	// Yes skips the delete confirmation prompt.
	Yes bool
}

// Push zips the selected groups that exist locally and uploads (or updates)
// the zip in the configured Drive folder.
func (s *Syncer) Push(ctx context.Context, cfg *config.Config, groups []string, opts PushOptions) error {
	wanted, err := cfg.ResolveFiles(groups)
	if err != nil {
		return err
	}

	var existing, missing []string
	for _, rel := range wanted {
		if _, err := os.Stat(filepath.Join(cfg.Root, rel)); err == nil {
			existing = append(existing, rel)
		} else {
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		s.printf("Note: not found locally, skipping: %s\n", strings.Join(missing, ", "))
	}
	if len(existing) == 0 {
		return fmt.Errorf("%w: none of the target files exist locally", domain.ErrNothingToUpload)
	}

	folder, err := s.requireFolder(ctx, cfg)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "gdrive-secrets-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, cfg.Drive.Filename)
	entries, err := s.archiver.Create(zipPath, cfg.Root, existing)
	if err != nil {
		return err
	}
	for _, name := range entries {
		s.printf("Adding %s to archive\n", name)
	}

	remote, err := s.store.FindFile(ctx, cfg.Drive.Filename, folder.ID)
	if err != nil {
		return err
	}
	fileID, action, err := s.store.Upload(ctx, zipPath, cfg.Drive.Filename, folder.ID, remote)
	if err != nil {
		return err
	}

	verb := "Uploaded new"
	if action == domain.ActionUpdated {
		verb = "Updated existing"
	}
	s.printf("%s '%s' in Drive folder '%s' (id=%s) — %s\n",
		verb, cfg.Drive.Filename, cfg.Drive.Folder, fileID, time.Now().Format("2006-01-02T15:04:05"))
	s.println("Push complete.")

	if opts.Delete {
		return s.deleteLocal(cfg, existing, opts.Yes)
	}
	return nil
}

func (s *Syncer) deleteLocal(cfg *config.Config, rels []string, yes bool) error {
	if !yes {
		if !s.confirm(fmt.Sprintf("Delete %d local file(s) that were just pushed? [y/N] ", len(rels))) {
			s.println("Not deleting local files.")
			return nil
		}
	}
	for _, rel := range rels {
		p := filepath.Join(cfg.Root, rel)
		if err := os.Remove(p); err != nil {
			return err
		}
		s.printf("Deleted %s\n", p)
	}
	s.println("Local cleanup complete.")
	return nil
}
