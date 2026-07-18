// Package config loads, validates, and scaffolds the per-repo
// .gdrive-sync.yaml file. It is pure with respect to the network: everything
// here touches only the local filesystem, which makes it straightforward to
// test against temporary directories.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aamoussou/gdrive-secrets-sync/internal/domain"
	"gopkg.in/yaml.v3"
)

// ConfigFilenames are the accepted config file names, in lookup order.
var ConfigFilenames = []string{".gdrive-sync.yaml", ".gdrive-sync.yml"}

// Template is the starter config written by Scaffold.
const Template = `# gdrive-secrets-sync config.
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

// Drive describes where the synced zip lives in Google Drive.
type Drive struct {
	Folder   string `yaml:"folder"`
	Filename string `yaml:"filename"`
}

type raw struct {
	Drive  Drive               `yaml:"drive"`
	Groups map[string][]string `yaml:"groups"`
}

// Config is the parsed, validated form of a .gdrive-sync.yaml file.
type Config struct {
	Path   string // the config file itself (absolute)
	Root   string // directory relative paths are resolved against
	Drive  Drive
	Groups map[string][]string
}

// GroupNames returns the config's group names in stable (sorted) order so
// output and file resolution are deterministic regardless of map iteration.
func (c *Config) GroupNames() []string {
	names := make([]string, 0, len(c.Groups))
	for name := range c.Groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResolveFiles flattens the given group names into a deduplicated, ordered
// list of relative file paths. If groupNames is empty, every group is used.
// It returns an error wrapping domain.ErrUnknownGroup if any name is unknown.
func (c *Config) ResolveFiles(groupNames []string) ([]string, error) {
	if len(groupNames) == 0 {
		groupNames = c.GroupNames()
	}

	var unknown []string
	for _, g := range groupNames {
		if _, ok := c.Groups[g]; !ok {
			unknown = append(unknown, g)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%w: %s. Known groups: %s",
			domain.ErrUnknownGroup,
			strings.Join(unknown, ", "),
			strings.Join(c.GroupNames(), ", "))
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

// FindConfig walks upward from start (default: cwd), like git looks for .git,
// returning the path to the first config file found or "" if none exists.
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
		for _, name := range ConfigFilenames {
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

// Load reads and validates a .gdrive-sync.yaml file at path. Validation
// failures wrap domain.ErrInvalidConfig.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var r raw
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", domain.ErrInvalidConfig, path, err)
	}

	if r.Drive.Folder == "" || r.Drive.Filename == "" {
		return nil, fmt.Errorf("%w: %s: 'drive.folder' and 'drive.filename' are required",
			domain.ErrInvalidConfig, path)
	}
	if len(r.Groups) == 0 {
		return nil, fmt.Errorf("%w: %s: at least one entry under 'groups' is required",
			domain.ErrInvalidConfig, path)
	}
	for name, files := range r.Groups {
		if len(files) == 0 {
			return nil, fmt.Errorf("%w: %s: group '%s' must be a non-empty list of file paths",
				domain.ErrInvalidConfig, path, name)
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	return &Config{
		Path:   absPath,
		Root:   filepath.Dir(absPath),
		Drive:  r.Drive,
		Groups: r.Groups,
	}, nil
}

// Scaffold writes the starter config template to path, refusing to overwrite
// an existing file.
func Scaffold(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists, not overwriting", path)
	}
	return os.WriteFile(path, []byte(Template), 0o644)
}
