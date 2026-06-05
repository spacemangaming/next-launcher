package changelog

import (
	"strings"
	"testing"

	"github.com/spacemangaming/next-launcher/internal/manifest"
)

func TestBuild(t *testing.T) {
	t.Run("basic changelog with updates and deletes", func(t *testing.T) {
		updates := []manifest.FileInfo{
			{Name: "file1.txt"},
			{Name: "file2.txt"},
		}
		deletedFiles := []string{"old.txt"}

		cfg := BuildConfig{
			Channel: "dev",
		}

		got := Build(updates, deletedFiles, cfg)

		// Check header
		if !strings.Contains(got, "Miriani-Next Update Changelog") {
			t.Error("Build() missing header")
		}
		if !strings.Contains(got, "Channel: dev") {
			t.Error("Build() missing channel")
		}
		if !strings.Contains(got, "Total changes: 3 files (2 updated, 1 deleted)") {
			t.Error("Build() missing or incorrect total changes")
		}

		// Check file lists
		if !strings.Contains(got, "+ file1.txt") {
			t.Error("Build() missing updated file1")
		}
		if !strings.Contains(got, "+ file2.txt") {
			t.Error("Build() missing updated file2")
		}
		if !strings.Contains(got, "- old.txt") {
			t.Error("Build() missing deleted file")
		}
	})

	t.Run("empty updates and deletes", func(t *testing.T) {
		cfg := BuildConfig{
			Channel: "stable",
		}

		got := Build(nil, nil, cfg)

		if !strings.Contains(got, "Total changes: 0 files (0 updated, 0 deleted)") {
			t.Error("Build() incorrect count for empty changes")
		}
	})

	t.Run("only updates", func(t *testing.T) {
		updates := []manifest.FileInfo{
			{Name: "new.txt"},
		}

		cfg := BuildConfig{
			Channel: "stable",
		}

		got := Build(updates, nil, cfg)

		if !strings.Contains(got, "Updated/Added (1 files)") {
			t.Error("Build() missing updates section")
		}
		if strings.Contains(got, "Deleted") {
			t.Error("Build() should not have deleted section when no deletions")
		}
	})

	t.Run("only deletes", func(t *testing.T) {
		deletedFiles := []string{"removed.txt"}

		cfg := BuildConfig{
			Channel: "dev",
		}

		got := Build(nil, deletedFiles, cfg)

		if strings.Contains(got, "Updated/Added") {
			t.Error("Build() should not have updates section when no updates")
		}
		if !strings.Contains(got, "Deleted (1 files)") {
			t.Error("Build() missing deleted section")
		}
	})
}
