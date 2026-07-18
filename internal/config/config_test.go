package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aamoussou/gdrive-secrets-sync/internal/domain"
)

func writeConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

const validBody = `
drive:
  folder: my-project
  filename: secrets.zip
groups:
  alpha:
    - a/one.txt
    - a/two.txt
  beta:
    - b/three.txt
    - a/one.txt
`

func TestLoadValid(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, ".gdrive-sync.yaml", validBody)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Drive.Folder != "my-project" || cfg.Drive.Filename != "secrets.zip" {
		t.Fatalf("drive = %+v", cfg.Drive)
	}
	if cfg.Root != dir {
		t.Fatalf("Root = %q, want %q", cfg.Root, dir)
	}
	if !filepath.IsAbs(cfg.Path) {
		t.Fatalf("Path should be absolute, got %q", cfg.Path)
	}
}

func TestLoadInvalid(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing drive":  "groups:\n  a:\n    - x.txt\n",
		"missing groups": "drive:\n  folder: f\n  filename: n.zip\n",
		"empty group":    "drive:\n  folder: f\n  filename: n.zip\ngroups:\n  a: []\n",
		"bad yaml":       "drive: [this is not valid",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeConfig(t, dir, "bad.yaml", body)
			_, err := Load(p)
			if !errors.Is(err, domain.ErrInvalidConfig) {
				t.Fatalf("want ErrInvalidConfig, got %v", err)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestGroupNamesSorted(t *testing.T) {
	c := &Config{Groups: map[string][]string{"zeta": {"z"}, "alpha": {"a"}, "mid": {"m"}}}
	got := c.GroupNames()
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupNames() = %v, want %v", got, want)
	}
}

func TestResolveFilesDedupAndOrder(t *testing.T) {
	c := &Config{Groups: map[string][]string{
		"alpha": {"a/one.txt", "a/two.txt"},
		"beta":  {"b/three.txt", "a/one.txt"},
	}}
	got, err := c.ResolveFiles([]string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	want := []string{"a/one.txt", "a/two.txt", "b/three.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveFiles = %v, want %v", got, want)
	}
}

func TestResolveFilesEmptyMeansAll(t *testing.T) {
	c := &Config{Groups: map[string][]string{"alpha": {"a"}, "beta": {"b"}}}
	got, err := c.ResolveFiles(nil)
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	// GroupNames is sorted (alpha, beta), so files come in that order.
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveFiles(nil) = %v, want %v", got, want)
	}
}

func TestResolveFilesUnknownGroup(t *testing.T) {
	c := &Config{Groups: map[string][]string{"alpha": {"a"}}}
	_, err := c.ResolveFiles([]string{"alpha", "ghost"})
	if !errors.Is(err, domain.ErrUnknownGroup) {
		t.Fatalf("want ErrUnknownGroup, got %v", err)
	}
}

func TestFindConfigWalksUpward(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, ".gdrive-sync.yaml", validBody)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	found, err := FindConfig(deep)
	if err != nil {
		t.Fatalf("FindConfig: %v", err)
	}
	want := filepath.Join(root, ".gdrive-sync.yaml")
	// Resolve symlinks (macOS /var -> /private/var) for a stable comparison.
	gotResolved, _ := filepath.EvalSymlinks(found)
	wantResolved, _ := filepath.EvalSymlinks(want)
	if gotResolved != wantResolved {
		t.Fatalf("FindConfig = %q, want %q", found, want)
	}
}

func TestFindConfigNotFound(t *testing.T) {
	found, err := FindConfig(t.TempDir())
	if err != nil {
		t.Fatalf("FindConfig: %v", err)
	}
	if found != "" {
		t.Fatalf("expected empty result, got %q", found)
	}
}

func TestScaffold(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".gdrive-sync.yaml")
	if err := Scaffold(p); err != nil {
		t.Fatalf("Scaffold: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read scaffolded: %v", err)
	}
	if string(data) != Template {
		t.Fatal("scaffolded content does not match template")
	}
	// The scaffolded config must itself be loadable.
	if _, err := Load(p); err != nil {
		t.Fatalf("scaffolded config should load: %v", err)
	}
}

func TestScaffoldRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, ".gdrive-sync.yaml", validBody)
	if err := Scaffold(p); err == nil {
		t.Fatal("Scaffold should refuse to overwrite existing file")
	}
}
