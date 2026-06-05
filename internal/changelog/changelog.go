package changelog

import (
	"fmt"
	"strings"
	"time"

	"github.com/spacemangaming/next-launcher/internal/manifest"
)

// BuildConfig holds configuration for building a changelog
type BuildConfig struct {
	Channel string
}

// Build creates a formatted changelog string
func Build(updates []manifest.FileInfo, deletedFiles []string, cfg BuildConfig) string {
	var changelog strings.Builder
	totalChanges := len(updates) + len(deletedFiles)

	changelog.WriteString("Miriani-Aura Update Changelog\n\n")
	changelog.WriteString(fmt.Sprintf("Channel: %s\n", cfg.Channel))
	changelog.WriteString(fmt.Sprintf("Update completed: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	changelog.WriteString(fmt.Sprintf("Total changes: %d files (%d updated, %d deleted)\n", totalChanges, len(updates), len(deletedFiles)))

	// Add file list
	changelog.WriteString("\n")
	changelog.WriteString(strings.Repeat("-", 60))
	changelog.WriteString("\nDetailed file changes:\n")
	changelog.WriteString(strings.Repeat("-", 60))
	changelog.WriteString("\n\n")

	if len(updates) > 0 {
		changelog.WriteString(fmt.Sprintf("Updated/Added (%d files):\n", len(updates)))
		for _, update := range updates {
			changelog.WriteString(fmt.Sprintf("  + %s\n", update.Name))
		}
		changelog.WriteString("\n")
	}

	if len(deletedFiles) > 0 {
		changelog.WriteString(fmt.Sprintf("Deleted (%d files):\n", len(deletedFiles)))
		for _, deleted := range deletedFiles {
			changelog.WriteString(fmt.Sprintf("  - %s\n", deleted))
		}
		changelog.WriteString("\n")
	}

	return changelog.String()
}
