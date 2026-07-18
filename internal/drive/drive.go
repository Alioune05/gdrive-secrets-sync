// Package drive wraps OAuth and the Google Drive v3 API calls this tool needs.
package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Full Drive access, not the narrower drive.file scope: the target folder is
// normally created by hand in the Drive UI rather than by this app, and
// drive.file can only ever see files/folders the app itself created. This is
// your own OAuth client on your own Google Cloud project, so the resulting
// token is only ever held by you.
var scopes = []string{drive.DriveScope}

// File is the subset of Drive file metadata this tool cares about.
type File struct {
	ID           string
	Name         string
	ModifiedTime string
	Size         string
}

// GetService runs (or refreshes) the OAuth flow and returns an authenticated
// Drive client.
func GetService(ctx context.Context, credentialsPath, tokenPath string) (*drive.Service, error) {
	credData, err := os.ReadFile(credentialsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"OAuth client secret not found at %s.\nSee README.md for one-time setup",
				credentialsPath,
			)
		}
		return nil, err
	}

	cfg, err := google.ConfigFromJSON(credData, scopes...)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", credentialsPath, err)
	}

	tok, err := tokenFromFile(tokenPath)
	if err != nil {
		tok, err = runAuthFlow(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokenPath, tok); err != nil {
			return nil, err
		}
	}

	tokenSource := cfg.TokenSource(ctx, tok)
	// Persist a refreshed token transparently, mirroring the Python
	// client's behavior of writing back whenever creds are refreshed.
	freshTok, err := tokenSource.Token()
	if err != nil {
		// Refresh token may be invalid/expired; fall back to a fresh
		// interactive flow rather than failing outright.
		freshTok, err = runAuthFlow(ctx, cfg)
		if err != nil {
			return nil, err
		}
		tokenSource = oauth2.StaticTokenSource(freshTok)
	}
	if freshTok.AccessToken != tok.AccessToken {
		if err := saveToken(tokenPath, freshTok); err != nil {
			return nil, err
		}
	}

	return drive.NewService(ctx, option.WithTokenSource(tokenSource))
}

func tokenFromFile(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	if err := json.NewDecoder(f).Decode(tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(tok)
}

// runAuthFlow starts a local HTTP server on a random port, opens the
// consent URL in the user's browser, and exchanges the resulting code for a
// token — the equivalent of google-auth-oauthlib's InstalledAppFlow.run_local_server.
func runAuthFlow(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	state := "gdrive-secrets-sync"
	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			errCh <- fmt.Errorf("invalid oauth state")
			return
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			fmt.Fprintln(w, "Authorization failed, you can close this tab.")
			errCh <- fmt.Errorf("authorization failed: %s", errMsg)
			return
		}
		code := r.URL.Query().Get("code")
		fmt.Fprintln(w, "Authorization complete, you can close this tab.")
		codeCh <- code
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	fmt.Println("Opening browser for Google authorization...")
	fmt.Println(authURL)
	openBrowser(authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange authorization code: %w", err)
	}
	return tok, nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// FindFolder looks up a Drive folder by exact name in My Drive.
func FindFolder(service *drive.Service, name string) (*File, error) {
	query := fmt.Sprintf(
		"mimeType = 'application/vnd.google-apps.folder' and name = '%s' and trashed = false",
		escapeQueryValue(name),
	)
	resp, err := service.Files.List().Q(query).Spaces("drive").Fields("files(id, name)").Do()
	if err != nil {
		return nil, err
	}
	if len(resp.Files) == 0 {
		return nil, nil
	}
	f := resp.Files[0]
	return &File{ID: f.Id, Name: f.Name}, nil
}

// RequireFolder is FindFolder but returns an error if the folder is missing.
func RequireFolder(service *drive.Service, name string) (*File, error) {
	folder, err := FindFolder(service, name)
	if err != nil {
		return nil, err
	}
	if folder == nil {
		return nil, fmt.Errorf(
			"Drive folder '%s' not found in My Drive.\nCreate it (or check the 'drive.folder' value in your config) and retry",
			name,
		)
	}
	return folder, nil
}

// FindFileInFolder returns the most-recently-modified file with the given
// name inside folderID, or nil if none exists.
func FindFileInFolder(service *drive.Service, filename, folderID string) (*File, error) {
	query := fmt.Sprintf("name = '%s' and '%s' in parents and trashed = false",
		escapeQueryValue(filename), escapeQueryValue(folderID))
	resp, err := service.Files.List().
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
	return &File{ID: f.Id, Name: f.Name, ModifiedTime: f.ModifiedTime, Size: formatSize(f.Size)}, nil
}

// DownloadFile streams a Drive file's contents to a local path.
func DownloadFile(service *drive.Service, fileID, destPath string) error {
	resp, err := service.Files.Get(fileID).Download()
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

// UploadOrUpdate creates filename in folderID, or updates it in place if
// existing is non-nil. Returns the resulting file ID and one of
// "created"/"updated".
func UploadOrUpdate(service *drive.Service, zipPath, filename, folderID string, existing *File) (string, string, error) {
	f, err := os.Open(zipPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	if existing != nil {
		_, err := service.Files.Update(existing.ID, &drive.File{}).Media(f).Do()
		if err != nil {
			return "", "", err
		}
		return existing.ID, "updated", nil
	}

	metadata := &drive.File{Name: filename, Parents: []string{folderID}}
	created, err := service.Files.Create(metadata).Media(f).Fields("id").Do()
	if err != nil {
		return "", "", err
	}
	return created.Id, "created", nil
}

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

func formatSize(size int64) string {
	if size == 0 {
		return "?"
	}
	return fmt.Sprintf("%d", size)
}
