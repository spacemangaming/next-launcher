//go:build embedded

package embedded

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Embedded release files - populated at build time.
// To build with embedded files:
//   1. Place Miriani-Aura.zip in internal/embedded/release/
//   2. Run: go build -tags embedded
//
// The ZIP should contain .manifest and version.json from the release.

//go:embed release/Miriani-Aura.zip
var embeddedZip []byte

// cachedVersion stores the version extracted from version.json in the ZIP
var cachedVersion string

func hasData() bool {
	return len(embeddedZip) > 0
}

func getVersion() string {
	if cachedVersion != "" {
		return cachedVersion
	}

	// Read version.json from the embedded ZIP
	reader, err := zip.NewReader(bytes.NewReader(embeddedZip), int64(len(embeddedZip)))
	if err != nil {
		return ""
	}

	// Find and strip prefix
	prefix := ""
	if len(reader.File) > 0 {
		firstPath := reader.File[0].Name
		if idx := strings.Index(firstPath, "/"); idx != -1 {
			prefix = firstPath[:idx+1]
		}
	}

	// Look for version.json
	for _, f := range reader.File {
		name := f.Name
		if prefix != "" && strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "version.json" {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}

			var ver struct {
				Major int `json:"major"`
				Minor int `json:"minor"`
				Patch int `json:"patch"`
			}
			if err := json.Unmarshal(data, &ver); err != nil {
				continue
			}
			cachedVersion = fmt.Sprintf("%d.%d.%02d", ver.Major, ver.Minor, ver.Patch)
			return cachedVersion
		}
	}

	return ""
}

func getZipData() []byte {
	return embeddedZip
}
