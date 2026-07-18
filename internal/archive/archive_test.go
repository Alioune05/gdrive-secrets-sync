package archive

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// makeTree writes files (rel path -> content) under root.
func makeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// zipEntries reads back the entry name -> content of a zip.
func zipEntries(t *testing.T, zipPath string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry: %v", err)
		}
		data := make([]byte, f.UncompressedSize64)
		_, _ = rc.Read(data)
		rc.Close()
		out[f.Name] = string(data)
	}
	return out
}

func TestCreateAndRoundTrip(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, map[string]string{
		"a/one.txt": "hello",
		"b/two.txt": "world",
	})

	zipPath := filepath.Join(t.TempDir(), "out.zip")
	names, err := Create(zipPath, root, []string{"a/one.txt", "b/two.txt"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Entry names must use forward slashes.
	if len(names) != 2 || names[0] != "a/one.txt" || names[1] != "b/two.txt" {
		t.Fatalf("names = %v", names)
	}

	entries := zipEntries(t, zipPath)
	if entries["a/one.txt"] != "hello" || entries["b/two.txt"] != "world" {
		t.Fatalf("zip round trip mismatch: %v", entries)
	}
}

func TestCreateMissingFile(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "out.zip")
	if _, err := Create(zipPath, t.TempDir(), []string{"does-not-exist.txt"}); err == nil {
		t.Fatal("expected error for missing source file")
	}
}

func TestExtractExactMatch(t *testing.T) {
	src := t.TempDir()
	makeTree(t, src, map[string]string{"a/one.txt": "hello", "b/two.txt": "world"})
	zipPath := filepath.Join(t.TempDir(), "out.zip")
	if _, err := Create(zipPath, src, []string{"a/one.txt", "b/two.txt"}); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	outcome, err := Extract(ExtractRequest{
		ZipPath:  zipPath,
		DestRoot: dest,
		Wanted:   map[string]bool{"a/one.txt": true}, // only ask for one
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(outcome.AllEntries) != 2 {
		t.Fatalf("AllEntries = %v", outcome.AllEntries)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Status != StatusWritten {
		t.Fatalf("Results = %+v", outcome.Results)
	}

	got, err := os.ReadFile(filepath.Join(dest, "a/one.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("extracted content = %q, err = %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "b/two.txt")); !os.IsNotExist(err) {
		t.Fatal("b/two.txt should not have been extracted")
	}
}

func TestExtractBasenameFallback(t *testing.T) {
	// A legacy flat zip: entry "one.txt" should map to wanted "nested/one.txt".
	src := t.TempDir()
	makeTree(t, src, map[string]string{"one.txt": "flat"})
	zipPath := filepath.Join(t.TempDir(), "flat.zip")
	if _, err := Create(zipPath, src, []string{"one.txt"}); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	outcome, err := Extract(ExtractRequest{
		ZipPath:      zipPath,
		DestRoot:     dest,
		Wanted:       map[string]bool{"nested/one.txt": true},
		WantedByBase: map[string]string{"one.txt": "nested/one.txt"},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("Results = %+v", outcome.Results)
	}
	got, err := os.ReadFile(filepath.Join(dest, "nested/one.txt"))
	if err != nil || string(got) != "flat" {
		t.Fatalf("content = %q err = %v", got, err)
	}
}

func TestExtractOverwritePolicy(t *testing.T) {
	src := t.TempDir()
	makeTree(t, src, map[string]string{"one.txt": "new"})
	zipPath := filepath.Join(t.TempDir(), "out.zip")
	if _, err := Create(zipPath, src, []string{"one.txt"}); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	makeTree(t, dest, map[string]string{"one.txt": "old"}) // pre-existing

	// Policy declines the overwrite.
	outcome, err := Extract(ExtractRequest{
		ZipPath:   zipPath,
		DestRoot:  dest,
		Wanted:    map[string]bool{"one.txt": true},
		Overwrite: func(string) bool { return false },
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Status != StatusSkipped {
		t.Fatalf("expected StatusSkipped, got %+v", outcome.Results)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "one.txt")); string(got) != "old" {
		t.Fatalf("file should be untouched, got %q", got)
	}

	// Policy accepts the overwrite.
	outcome, err = Extract(ExtractRequest{
		ZipPath:   zipPath,
		DestRoot:  dest,
		Wanted:    map[string]bool{"one.txt": true},
		Overwrite: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if outcome.Results[0].Status != StatusWritten {
		t.Fatalf("expected StatusWritten, got %+v", outcome.Results)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "one.txt")); string(got) != "new" {
		t.Fatalf("file should be overwritten, got %q", got)
	}
}

func TestExtractNilPolicyAlwaysOverwrites(t *testing.T) {
	src := t.TempDir()
	makeTree(t, src, map[string]string{"one.txt": "new"})
	zipPath := filepath.Join(t.TempDir(), "out.zip")
	if _, err := Create(zipPath, src, []string{"one.txt"}); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	makeTree(t, dest, map[string]string{"one.txt": "old"})

	if _, err := Extract(ExtractRequest{
		ZipPath:  zipPath,
		DestRoot: dest,
		Wanted:   map[string]bool{"one.txt": true},
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "one.txt")); string(got) != "new" {
		t.Fatalf("nil policy should overwrite, got %q", got)
	}
}

func TestExtractedFilesArePrivate(t *testing.T) {
	src := t.TempDir()
	makeTree(t, src, map[string]string{"secret.txt": "s"})
	zipPath := filepath.Join(t.TempDir(), "out.zip")
	if _, err := Create(zipPath, src, []string{"secret.txt"}); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := Extract(ExtractRequest{
		ZipPath:  zipPath,
		DestRoot: dest,
		Wanted:   map[string]bool{"secret.txt": true},
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("extracted file mode = %o, want 600", perm)
	}
}

func TestZipImplementsArchiverBehaviour(t *testing.T) {
	// The Zip wrapper must behave identically to the package functions.
	src := t.TempDir()
	makeTree(t, src, map[string]string{"one.txt": "x"})
	zipPath := filepath.Join(t.TempDir(), "out.zip")

	var z Zip
	names, err := z.Create(zipPath, src, []string{"one.txt"})
	if err != nil || len(names) != 1 {
		t.Fatalf("Zip.Create: names=%v err=%v", names, err)
	}
	dest := t.TempDir()
	outcome, err := z.Extract(ExtractRequest{
		ZipPath:  zipPath,
		DestRoot: dest,
		Wanted:   map[string]bool{"one.txt": true},
	})
	if err != nil || len(outcome.Results) != 1 {
		t.Fatalf("Zip.Extract: %+v err=%v", outcome, err)
	}
}
