package embedded

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacemangaming/next-launcher/internal/paths"
)

// HasData returns true if embedded release data is available.
// This is false for normal builds and true for builds with -tags embedded.
func HasData() bool {
	return hasData()
}

// GetVersion returns the embedded release version (e.g., "4.0.17").
// Returns empty string if no embedded data is available.
func GetVersion() string {
	return getVersion()
}

// ProgressFunc is called during extraction with current file index and total files.
type ProgressFunc func(current, total int, filename string)

// ExtractTo extracts embedded files to the target directory.
// Returns error if no embedded data is available.
func ExtractTo(targetDir string, progress ProgressFunc) error {
	data := getZipData()
	if len(data) == 0 {
		return fmt.Errorf("no embedded release data")
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("failed to open embedded zip: %w", err)
	}

	// Detect and strip GitHub archive prefix (e.g., "owner-repo-branch/")
	stripPrefix := detectStripPrefix(reader)

	total := len(reader.File)
	current := 0

	for _, f := range reader.File {
		// Strip GitHub archive prefix
		relPath := f.Name
		if stripPrefix != "" && strings.HasPrefix(relPath, stripPrefix) {
			relPath = strings.TrimPrefix(relPath, stripPrefix)
		}

		// Skip empty paths (the root directory itself)
		if relPath == "" {
			continue
		}

		current++
		if progress != nil {
			progress(current, total, relPath)
		}

		// Normalize path for the target platform
		targetPath := filepath.Join(targetDir, paths.Denormalize(relPath))

		// Security: prevent path traversal
		absTarget, err := filepath.Abs(targetPath)
		if err != nil {
			return fmt.Errorf("failed to resolve path %s: %w", relPath, err)
		}
		absTargetDir, err := filepath.Abs(targetDir)
		if err != nil {
			return fmt.Errorf("failed to resolve target dir: %w", err)
		}
		if !strings.HasPrefix(absTarget, absTargetDir) {
			return fmt.Errorf("path traversal attempt detected: %s", relPath)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(absTarget, f.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", relPath, err)
			}
			continue
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(absTarget), 0755); err != nil {
			return fmt.Errorf("failed to create parent dir for %s: %w", relPath, err)
		}

		// Extract file
		if err := extractFile(f, absTarget); err != nil {
			return fmt.Errorf("failed to extract %s: %w", relPath, err)
		}
	}

	return nil
}

// detectStripPrefix finds the common prefix directory in GitHub zipballs.
// GitHub archives have format: "owner-repo-ref/"
func detectStripPrefix(reader *zip.Reader) string {
	if len(reader.File) == 0 {
		return ""
	}

	firstPath := reader.File[0].Name
	idx := strings.Index(firstPath, "/")
	if idx == -1 {
		return ""
	}

	prefix := firstPath[:idx+1]

	// Verify all files have this prefix
	for _, f := range reader.File {
		if !strings.HasPrefix(f.Name, prefix) {
			return "" // Not all files have the prefix, don't strip
		}
	}

	return prefix
}

func extractFile(f *zip.File, targetPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}
