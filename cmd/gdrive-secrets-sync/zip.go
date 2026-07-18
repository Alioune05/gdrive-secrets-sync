package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aamoussou/gdrive-secrets-sync/internal/config"
)

// createZip writes a new deflated zip at zipPath containing each of the
// given paths (relative to root), preserving those relative paths as
// archive entry names.
func createZip(zipPath, root string, relPaths []string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	for _, rel := range relPaths {
		if err := addFileToZip(zw, root, rel); err != nil {
			return err
		}
		fmt.Printf("Adding %s to archive\n", rel)
	}
	return nil
}

func addFileToZip(zw *zip.Writer, root, rel string) error {
	full := filepath.Join(root, rel)
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	// Zip entries use forward slashes regardless of OS.
	header.Name = filepath.ToSlash(rel)
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// extractZip pulls entries out of the zip at zipPath that match one of the
// wanted relative paths (first by exact match, then falling back to
// matching by basename, for zips created before groups/subdirectories
// existed), writing them under cfg.Root. Prompts before overwriting an
// existing local file unless yes is true.
func extractZip(zipPath string, cfg *config.SyncConfig, wanted map[string]bool, wantedBasenames map[string]string, yes bool) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		fmt.Println("Zip contains: (empty)")
	} else {
		fmt.Printf("Zip contains: %s\n", strings.Join(names, ", "))
	}

	for _, f := range zr.File {
		posixName := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		var relDest string
		if wanted[posixName] {
			relDest = posixName
		} else if orig, ok := wantedBasenames[path.Base(posixName)]; ok {
			relDest = orig
		} else {
			continue
		}

		dest := filepath.Join(cfg.Root, relDest)
		if _, err := os.Stat(dest); err == nil && !yes {
			if !confirm(fmt.Sprintf("%s already exists locally. Overwrite? [y/N] ", dest)) {
				fmt.Printf("Skipping %s\n", dest)
				continue
			}
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := extractOne(f, dest); err != nil {
			return err
		}
		fmt.Printf("Wrote %s\n", dest)
	}

	return nil
}

func extractOne(f *zip.File, dest string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, src); err != nil {
		return err
	}
	return os.Chmod(dest, 0o600)
}
