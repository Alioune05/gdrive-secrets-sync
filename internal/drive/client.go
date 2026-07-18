// Package drive is the Google Drive adapter: it wraps OAuth and the Drive v3
// API calls the tool needs behind a small Client whose method set satisfies
// the port the syncer usecase depends on. All Drive-specific translation
// (query escaping, size formatting, metadata mapping into domain.RemoteFile)
// lives here so the rest of the app never imports the Google SDK.
package drive

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/aamoussou/gdrive-secrets-sync/internal/domain"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Full Drive access, not the narrower drive.file scope: the target folder is
// normally created by hand in the Drive UI rather than by this app, and
// drive.file can only ever see files/folders the app itself created. This is
// your own OAuth client on your own Google Cloud project, so the resulting
// token is only ever held by you.
var scopes = []string{drive.DriveScope}

// Client is an authenticated Google Drive client scoped to the operations the
// syncer usecase requires.
type Client struct {
	svc *drive.Service
}

// NewClient runs (or refreshes) the OAuth flow using the given credential and
// token files, then returns a ready-to-use Client.
func NewClient(ctx context.Context, credentialsPath, tokenPath string) (*Client, error) {
	ts, err := tokenSource(ctx, credentialsPath, tokenPath)
	if err != nil {
		return nil, err
	}
	svc, err := drive.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, err
	}
	return &Client{svc: svc}, nil
}

// FindFolder looks up a Drive folder by exact name in My Drive, returning nil
// if none exists.
func (c *Client) FindFolder(ctx context.Context, name string) (*domain.RemoteFile, error) {
	query := fmt.Sprintf(
		"mimeType = 'application/vnd.google-apps.folder' and name = '%s' and trashed = false",
		escapeQueryValue(name),
	)
	resp, err := c.svc.Files.List().Context(ctx).Q(query).Spaces("drive").Fields("files(id, name)").Do()
	if err != nil {
		return nil, err
	}
	if len(resp.Files) == 0 {
		return nil, nil
	}
	f := resp.Files[0]
	return &domain.RemoteFile{ID: f.Id, Name: f.Name}, nil
}

// FindFile returns the most-recently-modified file with the given name inside
// folderID, or nil if none exists.
func (c *Client) FindFile(ctx context.Context, filename, folderID string) (*domain.RemoteFile, error) {
	query := fmt.Sprintf("name = '%s' and '%s' in parents and trashed = false",
		escapeQueryValue(filename), escapeQueryValue(folderID))
	resp, err := c.svc.Files.List().
		Context(ctx).
		Q(query).
		Spaces("drive").
		Fields("files(id, name, modifiedTime, size)").
		Do()
	if err != nil {
		return nil, err
	}
	if len(resp.Files) == 0 {
		return nil, nil
	}
	files := resp.Files
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModifiedTime > files[j].ModifiedTime
	})
	f := files[0]
	return &domain.RemoteFile{
		ID:           f.Id,
		Name:         f.Name,
		ModifiedTime: f.ModifiedTime,
		Size:         formatSize(f.Size),
	}, nil
}

// Download streams a Drive file's contents to a local path.
func (c *Client) Download(ctx context.Context, fileID, destPath string) error {
	resp, err := c.svc.Files.Get(fileID).Context(ctx).Download()
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// Upload creates filename in folderID, or updates it in place if existing is
// non-nil. It returns the resulting file ID and the action taken.
func (c *Client) Upload(ctx context.Context, localPath, filename, folderID string, existing *domain.RemoteFile) (string, domain.UploadAction, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	if existing != nil {
		if _, err := c.svc.Files.Update(existing.ID, &drive.File{}).Context(ctx).Media(f).Do(); err != nil {
			return "", "", err
		}
		return existing.ID, domain.ActionUpdated, nil
	}

	metadata := &drive.File{Name: filename, Parents: []string{folderID}}
	created, err := c.svc.Files.Create(metadata).Context(ctx).Media(f).Fields("id").Do()
	if err != nil {
		return "", "", err
	}
	return created.Id, domain.ActionCreated, nil
}

// escapeQueryValue backslash-escapes the characters that are special inside a
// Drive query string literal.
func escapeQueryValue(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// formatSize renders a byte count for display, using "?" when Drive reports
// no size (0), which it does for some file types.
func formatSize(size int64) string {
	if size == 0 {
		return "?"
	}
	return fmt.Sprintf("%d", size)
}
