package drive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestTokenRoundTrip(t *testing.T) {
	// saveToken should create intermediate dirs and write a token that
	// tokenFromFile reads back faithfully.
	path := filepath.Join(t.TempDir(), "nested", "token.json")
	want := &oauth2.Token{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Round(time.Second),
	}
	if err := saveToken(path, want); err != nil {
		t.Fatalf("saveToken: %v", err)
	}

	// Written token file must be private (0600).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode = %o, want 600", perm)
	}

	got, err := tokenFromFile(path)
	if err != nil {
		t.Fatalf("tokenFromFile: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || got.TokenType != want.TokenType {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestTokenFromFileMissing(t *testing.T) {
	if _, err := tokenFromFile(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing token file")
	}
}

func TestTokenFromFileBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tokenFromFile(path); err == nil {
		t.Fatal("expected error for malformed token file")
	}
}

func TestTokenSourceMissingCredentials(t *testing.T) {
	_, err := tokenSource(context.Background(), filepath.Join(t.TempDir(), "creds.json"), filepath.Join(t.TempDir(), "tok.json"))
	if err == nil || !strings.Contains(err.Error(), "OAuth client secret not found") {
		t.Fatalf("expected friendly not-found error, got %v", err)
	}
}

func TestTokenSourceInvalidCredentials(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(credPath, []byte("{not valid oauth json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := tokenSource(context.Background(), credPath, filepath.Join(t.TempDir(), "tok.json"))
	if err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Fatalf("expected parse error, got %v", err)
	}
}
