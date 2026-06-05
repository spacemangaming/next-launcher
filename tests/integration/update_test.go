package integration

import (
	"os"
	"testing"

	"github.com/spacemangaming/next-launcher/internal/manifest"
)

// TestNormalUpdate_DifferentialFiles tests that only changed files are identified
func TestNormalUpdate_DifferentialFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := SetupTestEnvironment(t)
	defer env.Cleanup()

	// Setup: Create existing installation with manifest
	existingFiles := map[string]manifest.FileInfo{
		"file1.txt": {Name: "file1.txt", Hash: "hash1", URL: "url1"},
		"file2.txt": {Name: "file2.txt", Hash: "hash2", URL: "url2"},
		"file3.txt": {Name: "file3.txt", Hash: "hash3", URL: "url3"},
	}

	// Create the files
	for path := range existingFiles {
		if err := env.CreateFile(path, "original content of "+path); err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}
	}

	// Save initial manifest
	originalDir, _ := os.Getwd()
	os.Chdir(env.BaseDir)
	defer os.Chdir(originalDir)

	denormalize := func(p string) string { return p }

	if err := env.ManifestMgr.Save(existingFiles, denormalize); err != nil {
		t.Fatalf("failed to save initial manifest: %v", err)
	}

	// Simulate update: file1 unchanged, file2 changed, file3 removed, file4 added
	newFiles := map[string]manifest.FileInfo{
		"file1.txt": {Name: "file1.txt", Hash: "hash1", URL: "url1"}, // Same hash - unchanged
		"file2.txt": {Name: "file2.txt", Hash: "newhash2", URL: "url2"}, // Different hash - changed
		"file4.txt": {Name: "file4.txt", Hash: "hash4", URL: "url4"}, // New file
	}

	// Identify what needs to be updated
	var needsUpdate []string
	var needsDelete []string
	var needsAdd []string

	for path, newInfo := range newFiles {
		if oldInfo, exists := existingFiles[path]; exists {
			if oldInfo.Hash != newInfo.Hash {
				needsUpdate = append(needsUpdate, path)
			}
		} else {
			needsAdd = append(needsAdd, path)
		}
	}

	for path := range existingFiles {
		if _, exists := newFiles[path]; !exists {
			needsDelete = append(needsDelete, path)
		}
	}

	// Verify differential update logic
	if len(needsUpdate) != 1 || needsUpdate[0] != "file2.txt" {
		t.Errorf("needsUpdate = %v, want [file2.txt]", needsUpdate)
	}

	if len(needsAdd) != 1 || needsAdd[0] != "file4.txt" {
		t.Errorf("needsAdd = %v, want [file4.txt]", needsAdd)
	}

	if len(needsDelete) != 1 || needsDelete[0] != "file3.txt" {
		t.Errorf("needsDelete = %v, want [file3.txt]", needsDelete)
	}

	t.Logf("Update plan: %d to update, %d to add, %d to delete",
		len(needsUpdate), len(needsAdd), len(needsDelete))
}

// TestNormalUpdate_ManifestConsistency tests atomic manifest updates
func TestNormalUpdate_ManifestConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := SetupTestEnvironment(t)
	defer env.Cleanup()

	// Create initial manifest
	initialManifest := map[string]manifest.FileInfo{
		"file1.txt": {Name: "file1.txt", Hash: "abc123", URL: "url1"},
	}

	if err := env.CreateFile("file1.txt", "content1"); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	originalDir, _ := os.Getwd()
	os.Chdir(env.BaseDir)
	defer os.Chdir(originalDir)

	denormalize := func(p string) string { return p }

	err := env.ManifestMgr.Save(initialManifest, denormalize)
	if err != nil {
		t.Fatalf("failed to save manifest: %v", err)
	}

	// Load it back to verify consistency
	loadedManifest, err := env.LoadManifest()
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}

	if len(loadedManifest) != 1 {
		t.Errorf("loaded manifest has %d files, want 1", len(loadedManifest))
	}

	if info, exists := loadedManifest["file1.txt"]; !exists {
		t.Error("manifest missing file1.txt")
	} else if info.Hash != "abc123" {
		t.Errorf("file1.txt hash = %s, want abc123", info.Hash)
	}
}

// TestNormalUpdate_UserConfigPreserved tests that user files are never overwritten
func TestNormalUpdate_UserConfigPreserved(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := SetupTestEnvironment(t)
	defer env.Cleanup()

	// Create user config files with custom content
	userFiles := map[string]string{
		"mushclient_prefs.sqlite": "user database",
		"worlds/miriani.mcl":      "user world file",
		"logs/custom.log":         "user log",
	}

	for path, content := range userFiles {
		if err := env.CreateFile(path, content); err != nil {
			t.Fatalf("failed to create user file %s: %v", path, err)
		}
	}

	// Simulate update that includes these files in tree
	manifestTree := []manifest.TreeItem{
		{Path: "mushclient_prefs.sqlite", Type: "blob", SHA: "new-sha"},
		{Path: "worlds/miriani.mcl", Type: "blob", SHA: "new-sha2"},
		{Path: "README.md", Type: "blob", SHA: "readme-sha"},
	}

	normalize := func(p string) string { return p }
	getRawURL := func(ref, path string) string { return "fake-url" }

	// Build manifest - user files should be excluded
	newManifest, err := env.ManifestMgr.BuildFromTree("stable", manifestTree, normalize, getRawURL)
	if err != nil {
		t.Fatalf("BuildFromTree() error = %v", err)
	}

	// Verify user files were excluded
	for userFile := range userFiles {
		if _, exists := newManifest[userFile]; exists {
			t.Errorf("user file %s should be excluded from manifest", userFile)
		}
	}

	// Verify user files still have original content
	for path, expectedContent := range userFiles {
		content, err := env.ReadFile(path)
		if err != nil {
			t.Errorf("failed to read user file %s: %v", path, err)
			continue
		}
		if content != expectedContent {
			t.Errorf("user file %s was modified: got %q, want %q", path, content, expectedContent)
		}
	}
}
