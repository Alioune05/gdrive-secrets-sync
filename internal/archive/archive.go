// Package archive handles the zip create/extract mechanics the tool relies
// on. It performs no console I/O and asks no questions of the user directly:
// overwrite decisions are delegated to a caller-supplied policy, which keeps
// the package pure and easy to unit-test against temporary directories.
package archive

import (
	"archive/zip"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Zip is a stateless implementation of the syncer's Archiver port backed by
// the package-level Create/Extract functions. It exists so callers can depend
// on an interface (and swap in a fake in tests) while production wiring uses
// the real filesystem-backed zip logic.
type Zip struct{}

// Create implements the archiver port. See the package-level Create.
func (Zip) Create(zipPath, root string, relPaths []string) ([]string, error) {
	return Create(zipPath, root, relPaths)
}

// Extract implements the archiver port. See the package-level Extract.
func (Zip) Extract(req ExtractRequest) (ExtractOutcome, error) {
	return Extract(req)
}

// Create writes a new deflated zip at zipPath containing each of the given
// paths (relative to root), preserving those relative paths as archive entry
// names (always with forward slashes). It returns the entry names written.
func Create(zipPath, root string, relPaths []string) ([]string, error) {
	out, err := os.Create(zipPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	zw := zip.NewWriter(out)

	written := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		name, err := addFile(zw, root, rel)
		if err != nil {
			zw.Close()
			return nil, err
		}
		written = append(written, name)
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return written, nil
}

func addFile(zw *zip.Writer, root, rel string) (string, error) {
	full := filepath.Join(root, rel)
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return "", err
	}
	// Zip entries use forward slashes regardless of OS.
	header.Name = filepath.ToSlash(rel)
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(w, f); err != nil {
		return "", err
	}
	return header.Name, nil
}

// Status describes what Extract did with a single archive entry.
type Status int

const (
	// StatusWritten means the entry was written to disk.
	StatusWritten Status = iota
	// StatusSkipped means the entry matched but the overwrite policy declined.
	StatusSkipped
)

// EntryResult reports the outcome for one extracted (matched) archive entry.
type EntryResult struct {
	Entry  string // the archive entry name
	Dest   string // absolute-ish destination path it maps to
	Status Status
}

// ExtractRequest configures a selective extract.
type ExtractRequest struct {
	ZipPath  string
	DestRoot string
	// Wanted matches archive entries by their exact (cleaned, posix)
	// relative path.
	Wanted map[string]bool
	// WantedByBase matches by basename, mapping it to the destination
	// relative path. Used as a fallback for legacy flat zips created before
	// groups/subdirectories existed.
	WantedByBase map[string]string
	// Overwrite is consulted before overwriting an existing local file; it
	// returns true to overwrite. If nil, existing files are always
	// overwritten.
	Overwrite func(dest string) bool
}

// ExtractOutcome is the full result of an Extract call.
type ExtractOutcome struct {
	// AllEntries lists every entry present in the archive, in order.
	AllEntries []string
	// Results lists only the entries that matched the request.
	Results []EntryResult
}

// Extract pulls entries out of the zip that match the request (first by exact
// relative path, then by basename), writing them under DestRoot with 0600
// permissions. Non-matching entries are ignored.
func Extract(req ExtractRequest) (ExtractOutcome, error) {
	zr, err := zip.OpenReader(req.ZipPath)
	if err != nil {
		return ExtractOutcome{}, err
	}
	defer zr.Close()

	outcome := ExtractOutcome{AllEntries: make([]string, 0, len(zr.File))}
	for _, f := range zr.File {
		outcome.AllEntries = append(outcome.AllEntries, f.Name)
	}

	for _, f := range zr.File {
		posixName := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		var relDest string
		switch {
		case req.Wanted[posixName]:
			relDest = posixName
		default:
			orig, ok := req.WantedByBase[path.Base(posixName)]
			if !ok {
				continue
			}
			relDest = orig
		}

		dest := filepath.Join(req.DestRoot, relDest)
		if _, err := os.Stat(dest); err == nil && req.Overwrite != nil && !req.Overwrite(dest) {
			outcome.Results = append(outcome.Results, EntryResult{Entry: f.Name, Dest: dest, Status: StatusSkipped})
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return outcome, err
		}
		if err := writeEntry(f, dest); err != nil {
			return outcome, err
		}
		outcome.Results = append(outcome.Results, EntryResult{Entry: f.Name, Dest: dest, Status: StatusWritten})
	}

	return outcome, nil
}

func writeEntry(f *zip.File, dest string) error {
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
