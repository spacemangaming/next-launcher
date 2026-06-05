package integration

import (
	"os"
	"testing"

	"github.com/spacemangaming/next-launcher/internal/github"
	"github.com/spacemangaming/next-launcher/internal/install"
	"github.com/spacemangaming/next-launcher/internal/manifest"
)

// TestFreshInstallation_CompleteFlow tests a complete fresh installation
func TestFreshInstallation_CompleteFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := SetupTestEnvironment(t)
	defer env.Cleanup()

	// Setup mock GitHub tree with files
	githubTree := []github.TreeItem{
		{Path: "README.md", Type: "blob", SHA: "readme-sha"},
		{Path: "src/main.go", Type: "blob", SHA: "main-sha"},
		{Path: "worlds/plugins/plugin.xml", Type: "blob", SHA: "plugin-sha"},
		{Path: ".gitignore", Type: "blob", SHA: "gitignore-sha"}, // Should be excluded
	}

	env.SetupMockGitHubTree("stable", githubTree)

	// Step 1: Verify directory is not installed
	if install.IsInstalled(env.BaseDir) {
		t.Fatal("directory should not be marked as installed initially")
	}

	// Step 2: Convert GitHub tree to manifest tree items
	manifestTree := make([]manifest.TreeItem, len(githubTree))
	for i, item := range githubTree {
		manifestTree[i] = manifest.TreeItem{
			Path: item.Path,
			Type: item.Type,
			SHA:  item.SHA,
		}
	}

	// Step 3: Build manifest from mock tree
	normalize := func(p string) string {
		return p
	}
	getRawURL := func(ref, path string) string {
		return env.GitHubServer.URL + "/" + path
	}

	newManifest, err := env.ManifestMgr.BuildFromTree("stable", manifestTree, normalize, getRawURL)
	if err != nil {
		t.Fatalf("BuildFromTree() error = %v", err)
	}

	// Step 4: Create files locally (simulating download)
	for path := range newManifest {
		if err := env.CreateFile(path, "content of "+path); err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}
	}

	// Step 5: Save manifest
	originalDir, _ := os.Getwd()
	os.Chdir(env.BaseDir)
	defer os.Chdir(originalDir)

	denormalize := func(p string) string {
		return p
	}

	err = env.ManifestMgr.Save(newManifest, denormalize)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Step 6: Save channel
	err = env.ChannelMgr.Save("stable")
	if err != nil {
		t.Fatalf("channel.Save() error = %v", err)
	}

	// Step 7: Create MUSHclient.exe marker
	err = env.CreateFile("MUSHclient.exe", "fake mushclient")
	if err != nil {
		t.Fatalf("failed to create MUSHclient.exe: %v", err)
	}

	// Step 8: Create batch files
	err = install.CreateChannelSwitchBatchFiles(env.BaseDir)
	if err != nil {
		t.Fatalf("CreateChannelSwitchBatchFiles() error = %v", err)
	}

	// Verify: Manifest exists and has correct files
	env.AssertFileExists(".manifest")

	loadedManifest, err := env.LoadManifest()
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}

	// Should have 3 files (README.md, src/main.go, worlds/plugins/plugin.xml)
	// .gitignore should be excluded
	if len(loadedManifest) != 3 {
		t.Errorf("manifest should have 3 files, got %d", len(loadedManifest))
	}

	// Verify: Channel file exists
	env.AssertFileExists(".update-channel")

	loadedChannel, err := env.ChannelMgr.Load()
	if err != nil {
		t.Fatalf("failed to load channel: %v", err)
	}

	if loadedChannel != "stable" {
		t.Errorf("channel = %q, want stable", loadedChannel)
	}

	// Verify: Installation is detected
	if !install.IsInstalled(env.BaseDir) {
		t.Error("directory should be marked as installed")
	}

	// Verify: Batch files exist
	env.AssertFileExists("Switch to Stable.bat")
	env.AssertFileExists("Switch to Dev.bat")
	env.AssertFileExists("Switch to Any Channel.bat")

	// Verify: Files were created
	env.AssertFileExists("README.md")
	env.AssertFileExists("src/main.go")
	env.AssertFileExists("worlds/plugins/plugin.xml")

	// Verify: Excluded file was not added to manifest
	if _, exists := loadedManifest[".gitignore"]; exists {
		t.Error("manifest should not include .gitignore (excluded file)")
	}
}

// TestFreshInstallation_UserConfigPreservation tests that user files aren't overwritten
func TestFreshInstallation_UserConfigPreservation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := SetupTestEnvironment(t)
	defer env.Cleanup()

	// Create existing user config files
	userFiles := []string{
		"mushclient_prefs.sqlite",
		"mushclient.ini",
		"worlds/miriani.mcl",
		"logs/game.log",
	}

	for _, file := range userFiles {
		content := "original user content for " + file
		if err := env.CreateFile(file, content); err != nil {
			t.Fatalf("failed to create user file %s: %v", file, err)
		}
	}

	// Simulate update that would try to overwrite these files
	// Build manifest that includes user files
	manifestTree := []manifest.TreeItem{
		{Path: "mushclient_prefs.sqlite", Type: "blob", SHA: "new-sha"},
		{Path: "worlds/miriani.mcl", Type: "blob", SHA: "new-sha2"},
		{Path: "README.md", Type: "blob", SHA: "readme-sha"},
	}

	normalize := func(p string) string { return p }
	getRawURL := func(ref, path string) string { return "fake-url" }

	newManifest, err := env.ManifestMgr.BuildFromTree("stable", manifestTree, normalize, getRawURL)
	if err != nil {
		t.Fatalf("BuildFromTree() error = %v", err)
	}

	// User files should be excluded from manifest
	if _, exists := newManifest["mushclient_prefs.sqlite"]; exists {
		t.Error("mushclient_prefs.sqlite should be excluded from manifest")
	}

	if _, exists := newManifest["worlds/miriani.mcl"]; exists {
		t.Error("worlds/miriani.mcl should be excluded from manifest")
	}

	// README.md should be included (not a user file)
	if _, exists := newManifest["README.md"]; !exists {
		t.Error("README.md should be included in manifest")
	}

	// Verify original files are unchanged
	content, _ := env.ReadFile("mushclient_prefs.sqlite")
	if content != "original user content for mushclient_prefs.sqlite" {
		t.Error("user file was modified")
	}
}
