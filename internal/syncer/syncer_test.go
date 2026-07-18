package syncer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aamoussou/gdrive-secrets-sync/internal/archive"
	"github.com/aamoussou/gdrive-secrets-sync/internal/config"
	"github.com/aamoussou/gdrive-secrets-sync/internal/domain"
)

// --- fakes -----------------------------------------------------------------

type fakeStore struct {
	folder    *domain.RemoteFile
	folderErr error

	file    *domain.RemoteFile
	fileErr error

	downloadErr    error
	downloadedID   string
	downloadedDest string

	uploadID       string
	uploadAction   domain.UploadAction
	uploadErr      error
	uploadExisting *domain.RemoteFile
	uploadCalled   bool
}

func (f *fakeStore) FindFolder(_ context.Context, _ string) (*domain.RemoteFile, error) {
	return f.folder, f.folderErr
}
func (f *fakeStore) FindFile(_ context.Context, _, _ string) (*domain.RemoteFile, error) {
	return f.file, f.fileErr
}
func (f *fakeStore) Download(_ context.Context, fileID, destPath string) error {
	f.downloadedID = fileID
	f.downloadedDest = destPath
	if f.downloadErr != nil {
		return f.downloadErr
	}
	// Simulate a downloaded artifact so the caller's temp handling is exercised.
	return os.WriteFile(destPath, []byte("zip-bytes"), 0o600)
}
func (f *fakeStore) Upload(_ context.Context, _, _, _ string, existing *domain.RemoteFile) (string, domain.UploadAction, error) {
	f.uploadCalled = true
	f.uploadExisting = existing
	return f.uploadID, f.uploadAction, f.uploadErr
}

type fakeArchiver struct {
	createNames  []string
	createErr    error
	createRel    []string
	createCalled bool

	extractOutcome archive.ExtractOutcome
	extractErr     error
	extractReq     archive.ExtractRequest
	extractCalled  bool
}

func (f *fakeArchiver) Create(_, _ string, relPaths []string) ([]string, error) {
	f.createCalled = true
	f.createRel = relPaths
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createNames != nil {
		return f.createNames, nil
	}
	return relPaths, nil
}
func (f *fakeArchiver) Extract(req archive.ExtractRequest) (archive.ExtractOutcome, error) {
	f.extractCalled = true
	f.extractReq = req
	return f.extractOutcome, f.extractErr
}

// --- helpers ---------------------------------------------------------------

// newConfig builds a Config rooted at a fresh temp dir, creating the given
// present files on disk. groups maps group name -> relative paths.
func newConfig(t *testing.T, groups map[string][]string, present ...string) *config.Config {
	t.Helper()
	root := t.TempDir()
	for _, rel := range present {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &config.Config{
		Path:   filepath.Join(root, ".gdrive-sync.yaml"),
		Root:   root,
		Drive:  config.Drive{Folder: "folder", Filename: "secrets.zip"},
		Groups: groups,
	}
}

func folderFile() *domain.RemoteFile { return &domain.RemoteFile{ID: "folder-id", Name: "folder"} }

// --- Status ----------------------------------------------------------------

func TestStatusReportsLocalAndRemote(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"present.txt", "missing.txt"}}, "present.txt")
	store := &fakeStore{
		folder: folderFile(),
		file:   &domain.RemoteFile{ID: "fid", Name: "secrets.zip", ModifiedTime: "2026-01-01T00:00:00Z", Size: "42"},
	}
	var out bytes.Buffer
	s := New(store, &fakeArchiver{}, &out, nil)

	if err := s.Status(context.Background(), cfg, nil); err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := out.String()
	for _, want := range []string{"present.txt: present", "missing.txt: MISSING", "'secrets.zip' present (id=fid", "size=42 bytes"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestStatusRemoteMissing(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a.txt"}})
	store := &fakeStore{folder: folderFile(), file: nil}
	var out bytes.Buffer
	s := New(store, &fakeArchiver{}, &out, nil)

	if err := s.Status(context.Background(), cfg, nil); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(out.String(), "'secrets.zip' NOT found") {
		t.Fatalf("expected NOT found line, got:\n%s", out.String())
	}
}

func TestStatusFolderNotFound(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a.txt"}})
	store := &fakeStore{folder: nil}
	s := New(store, &fakeArchiver{}, nil, nil)

	err := s.Status(context.Background(), cfg, nil)
	if !errors.Is(err, domain.ErrFolderNotFound) {
		t.Fatalf("want ErrFolderNotFound, got %v", err)
	}
}

func TestStatusUnknownGroup(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a.txt"}})
	store := &fakeStore{folder: folderFile()}
	s := New(store, &fakeArchiver{}, nil, nil)

	err := s.Status(context.Background(), cfg, []string{"ghost"})
	if !errors.Is(err, domain.ErrUnknownGroup) {
		t.Fatalf("want ErrUnknownGroup, got %v", err)
	}
}

// --- Pull ------------------------------------------------------------------

func TestPullRemoteNotFound(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a.txt"}})
	store := &fakeStore{folder: folderFile(), file: nil}
	s := New(store, &fakeArchiver{}, nil, nil)

	err := s.Pull(context.Background(), cfg, nil, true)
	if !errors.Is(err, domain.ErrRemoteNotFound) {
		t.Fatalf("want ErrRemoteNotFound, got %v", err)
	}
}

func TestPullSuccess(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a/one.txt"}})
	store := &fakeStore{folder: folderFile(), file: &domain.RemoteFile{ID: "fid", Name: "secrets.zip", Size: "10"}}
	arch := &fakeArchiver{extractOutcome: archive.ExtractOutcome{
		AllEntries: []string{"a/one.txt"},
		Results:    []archive.EntryResult{{Entry: "a/one.txt", Dest: filepath.Join(cfg.Root, "a/one.txt"), Status: archive.StatusWritten}},
	}}
	var out bytes.Buffer
	s := New(store, arch, &out, nil)

	if err := s.Pull(context.Background(), cfg, nil, true); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if store.downloadedID != "fid" {
		t.Errorf("Download called with id %q, want fid", store.downloadedID)
	}
	if !arch.extractCalled {
		t.Error("archiver.Extract was not called")
	}
	got := out.String()
	for _, want := range []string{"Found 'secrets.zip'", "Zip contains: a/one.txt", "Wrote", "Pull complete."} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPullOverwritePolicyDependsOnYes(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a.txt"}})
	store := &fakeStore{folder: folderFile(), file: &domain.RemoteFile{ID: "fid"}}

	// yes = true -> no overwrite policy (always overwrite).
	arch := &fakeArchiver{}
	s := New(store, arch, nil, func(string) bool { return true })
	if err := s.Pull(context.Background(), cfg, nil, true); err != nil {
		t.Fatal(err)
	}
	if arch.extractReq.Overwrite != nil {
		t.Error("with -y, Overwrite policy should be nil")
	}

	// yes = false -> an overwrite policy that defers to confirm.
	arch = &fakeArchiver{}
	confirmed := false
	s = New(store, arch, nil, func(string) bool { confirmed = true; return true })
	if err := s.Pull(context.Background(), cfg, nil, false); err != nil {
		t.Fatal(err)
	}
	if arch.extractReq.Overwrite == nil {
		t.Fatal("without -y, Overwrite policy should be set")
	}
	if !arch.extractReq.Overwrite("some/dest") || !confirmed {
		t.Error("Overwrite policy should route through confirm")
	}
}

func TestPullEmptyZipMessage(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a.txt"}})
	store := &fakeStore{folder: folderFile(), file: &domain.RemoteFile{ID: "fid"}}
	arch := &fakeArchiver{extractOutcome: archive.ExtractOutcome{}}
	var out bytes.Buffer
	s := New(store, arch, &out, nil)

	if err := s.Pull(context.Background(), cfg, nil, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Zip contains: (empty)") {
		t.Fatalf("expected empty-zip message, got:\n%s", out.String())
	}
}

// --- Push ------------------------------------------------------------------

func TestPushNothingToUpload(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"missing.txt"}}) // nothing on disk
	store := &fakeStore{folder: folderFile()}
	s := New(store, &fakeArchiver{}, nil, nil)

	err := s.Push(context.Background(), cfg, nil, PushOptions{})
	if !errors.Is(err, domain.ErrNothingToUpload) {
		t.Fatalf("want ErrNothingToUpload, got %v", err)
	}
}

func TestPushSkipsMissingAndZipsExisting(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"here.txt", "gone.txt"}}, "here.txt")
	store := &fakeStore{folder: folderFile(), uploadID: "new-id", uploadAction: domain.ActionCreated}
	arch := &fakeArchiver{}
	var out bytes.Buffer
	s := New(store, arch, &out, nil)

	if err := s.Push(context.Background(), cfg, nil, PushOptions{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(arch.createRel) != 1 || arch.createRel[0] != "here.txt" {
		t.Errorf("Create should zip only existing files, got %v", arch.createRel)
	}
	got := out.String()
	for _, want := range []string{"not found locally, skipping: gone.txt", "Adding here.txt to archive", "Uploaded new", "Push complete."} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPushUpdatesExisting(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"here.txt"}}, "here.txt")
	existing := &domain.RemoteFile{ID: "old-id", Name: "secrets.zip"}
	store := &fakeStore{folder: folderFile(), file: existing, uploadID: "old-id", uploadAction: domain.ActionUpdated}
	var out bytes.Buffer
	s := New(store, &fakeArchiver{}, &out, nil)

	if err := s.Push(context.Background(), cfg, nil, PushOptions{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if store.uploadExisting != existing {
		t.Error("Upload should receive the existing remote file for update")
	}
	if !strings.Contains(out.String(), "Updated existing") {
		t.Fatalf("expected 'Updated existing', got:\n%s", out.String())
	}
}

func TestPushDeleteWithConfirmation(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"here.txt"}}, "here.txt")
	store := &fakeStore{folder: folderFile(), uploadID: "id", uploadAction: domain.ActionCreated}
	target := filepath.Join(cfg.Root, "here.txt")

	// Confirm declines: file must survive.
	s := New(store, &fakeArchiver{}, nil, func(string) bool { return false })
	if err := s.Push(context.Background(), cfg, nil, PushOptions{Delete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file should survive a declined delete: %v", err)
	}

	// Confirm accepts: file is removed.
	s = New(store, &fakeArchiver{}, nil, func(string) bool { return true })
	if err := s.Push(context.Background(), cfg, nil, PushOptions{Delete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted after confirmed delete, stat err: %v", err)
	}
}

func TestPushDeleteYesSkipsPrompt(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"here.txt"}}, "here.txt")
	store := &fakeStore{folder: folderFile(), uploadID: "id", uploadAction: domain.ActionCreated}
	target := filepath.Join(cfg.Root, "here.txt")

	promptCalled := false
	s := New(store, &fakeArchiver{}, nil, func(string) bool { promptCalled = true; return false })
	if err := s.Push(context.Background(), cfg, nil, PushOptions{Delete: true, Yes: true}); err != nil {
		t.Fatal(err)
	}
	if promptCalled {
		t.Error("with -y, delete should not prompt")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted with -y, stat err: %v", err)
	}
}

func TestNewNilDefaultsDoNotPanic(t *testing.T) {
	cfg := newConfig(t, map[string][]string{"g": {"a.txt"}})
	store := &fakeStore{folder: folderFile(), file: nil}
	s := New(store, &fakeArchiver{}, nil, nil) // nil out + nil confirm

	// Should route output to io.Discard and treat confirm as always-yes.
	if err := s.Status(context.Background(), cfg, nil); err != nil {
		t.Fatalf("Status with nil deps: %v", err)
	}
}
