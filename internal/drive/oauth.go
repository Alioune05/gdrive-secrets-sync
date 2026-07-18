package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// tokenSource builds an oauth2.TokenSource from a client-secret file and a
// cached token, running the interactive browser flow when no valid token is
// available and transparently persisting any refreshed token back to disk.
func tokenSource(ctx context.Context, credentialsPath, tokenPath string) (oauth2.TokenSource, error) {
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

	ts := cfg.TokenSource(ctx, tok)
	// Persist a refreshed token transparently, mirroring the Python client's
	// behavior of writing back whenever creds are refreshed.
	freshTok, err := ts.Token()
	if err != nil {
		// Refresh token may be invalid/expired; fall back to a fresh
		// interactive flow rather than failing outright.
		freshTok, err = runAuthFlow(ctx, cfg)
		if err != nil {
			return nil, err
		}
		ts = oauth2.StaticTokenSource(freshTok)
	}
	if freshTok.AccessToken != tok.AccessToken {
		if err := saveToken(tokenPath, freshTok); err != nil {
			return nil, err
		}
	}
	return ts, nil
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

// runAuthFlow starts a local HTTP server on a random port, opens the consent
// URL in the user's browser, and exchanges the resulting code for a token —
// the equivalent of google-auth-oauthlib's InstalledAppFlow.run_local_server.
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
