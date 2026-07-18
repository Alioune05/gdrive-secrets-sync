// Package config loads and scaffolds the per-repo .gdrive-sync.yaml file.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

var configFilenames = []string{".gdrive-sync.yaml", ".gdrive-sync.yml"}

const template = `# gdrive-secrets-sync config.
# Paths under each group are relative to this file's directory (your repo root).
# Run ` + "`gdrive-secrets-sync status`" + ` after editing this to sanity-check it.

drive:
  folder: my-project        # Folder name at the root of your Google Drive
  filename: my-secrets.zip  # Zip file name inside that folder

groups:
  example-group:
    - path/to/secret-file.txt
    - another/secret-dir/key.json
`

// DriveConfig describes where the synced zip lives in Google Drive.
type DriveConfig struct {
	Folder   string `yaml:"folder"`
	Filename string `yaml:"filename"`
}

type rawConfig struct {
	Drive  DriveConfig         `yaml:"drive"`
	Groups map[string][]string `yaml:"groups"`
}

// SyncConfig is the parsed, validated form of a .gdrive-sync.yaml file.
type SyncConfig struct {
	Path   string // the config file itself
	Root   string // directory relative paths are resolved against
	Drive  DriveConfig
	Groups map[string][]string
}

// GroupNames returns the config's group names in a stable order.
func (c *SyncConfig) GroupNames() []string {
	names := make([]string, 0, len(c.Groups))
	for name := range c.Groups {
		names = append(names, name)
	}
	return names
}

// ResolveFiles flattens the given group names into a deduplicated, ordered
// list of relative file paths. Exits the process with an error message if
// any group name is unknown.
func (c *SyncConfig) ResolveFiles(groupNames []string) ([]string, error) {
	var unknown []string
	for _, g := range groupNames {
		if _, ok := c.Groups[g]; !ok {
			unknown = append(unknown, g)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown group(s): %s. Known groups: %s",
			joinStrings(unknown, ", "), joinStrings(c.GroupNames(), ", "))
	}

	seen := map[string]bool{}
	var result []string
	for _, group := range groupNames {
		for _, rel := range c.Groups[group] {
			if !seen[rel] {
				seen[rel] = true
				result = append(result, rel)
			}
		}
	}
	return result, nil
}

func joinStrings(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

// FindConfig walks upward from start (default: cwd), like git looks for .git.
func FindConfig(start string) (string, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		for _, name := range configFilenames {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// Load reads and validates a .gdrive-sync.yaml file at path.
func Load(path string) (*SyncConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}

	if raw.Drive.Folder == "" || raw.Drive.Filename == "" {
		return nil, fmt.Errorf("%s: 'drive.folder' and 'drive.filename' are required", path)
	}

	if len(raw.Groups) == 0 {
		return nil, fmt.Errorf("%s: at least one entry under 'groups' is required", path)
	}
	for name, files := range raw.Groups {
		if len(files) == 0 {
			return nil, fmt.Errorf("%s: group '%s' must be a non-empty list of file paths", path, name)
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	return &SyncConfig{
		Path:   absPath,
		Root:   filepath.Dir(absPath),
		Drive:  raw.Drive,
		Groups: raw.Groups,
	}, nil
}

// Scaffold writes a starter config template to path, refusing to overwrite
// an existing file.
func Scaffold(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists, not overwriting", path)
	}
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote template config to %s.\n", path)
	fmt.Println("Edit it, then run 'gdrive-secrets-sync status' to sanity-check it.")
	return nil
}
