package selfupdate

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Config holds the configuration for self-update
type Config struct {
	ReleasesAPIURL string
	BinaryURL      string
	CurrentVersion string
}

// GitHubRelease represents the GitHub API response for a release
type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// DefaultConfig returns the default self-update configuration
func DefaultConfig(currentVersion string) Config {
	return Config{
		ReleasesAPIURL: "https://api.github.com/repos/spacemangaming/next-launcher/releases/latest",
		BinaryURL:      "https://github.com/spacemangaming/next-launcher/releases/latest/download/miriani.exe",
		CurrentVersion: currentVersion,
	}
}

// Check checks for a new version of the updater and replaces it if available.
// This function fails silently with a short timeout to avoid blocking the main update process.
// Returns true if the updater was replaced and a restart is needed.
func Check(cfg Config) error {
	// Get the path of the current executable
	exePath, err := os.Executable()
	if err != nil {
		return nil // Silent failure - not critical
	}

	// Create a client with a short timeout for version check
	quickClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     10 * time.Second,
			DisableCompression:  false,
		},
	}

	// Make a request to GitHub releases API
	req, err := http.NewRequest("GET", cfg.ReleasesAPIURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "next-launcher")

	resp, err := quickClient.Do(req)
	if err != nil {
		return nil // Silent failure - network issues, server down, etc.
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil // Silent failure - API error
	}

	// Parse the release info
	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil
	}

	// Extract version from tag (e.g., "v1.2.3" -> "1.2.3")
	remoteVersion := strings.TrimPrefix(release.TagName, "v")
	if remoteVersion == "" || remoteVersion == cfg.CurrentVersion {
		return nil // No update available
	}

	// Find the binary asset URL
	binaryURL := cfg.BinaryURL
	for _, asset := range release.Assets {
		if asset.Name == "miriani.exe" {
			binaryURL = asset.BrowserDownloadURL
			break
		}
	}

	// Update available - download and replace
	return downloadAndReplace(binaryURL, exePath)
}

// downloadAndReplace downloads the new binary and replaces the current executable.
// We trust GitHub's HTTPS, so no additional hash verification is needed.
func downloadAndReplace(binaryURL string, exePath string) error {
	downloadClient := &http.Client{Timeout: 60 * time.Second}

	// Download new binary
	resp, err := downloadClient.Get(binaryURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	// Basic sanity check - should be a reasonable size for an exe
	if len(data) < 1024*1024 { // Less than 1MB is suspicious
		return nil
	}

	// Replace the executable
	oldExe := exePath + ".old"
	_ = os.Remove(oldExe)
	if err := os.Rename(exePath, oldExe); err != nil {
		return nil
	}

	if err := os.WriteFile(exePath, data, 0755); err != nil {
		_ = os.Rename(oldExe, exePath)
		return nil
	}

	// Restart with same arguments
	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "UPDATER_CLEANUP_OLD=1")

	if err := cmd.Start(); err != nil {
		_ = os.Remove(exePath)
		_ = os.Rename(oldExe, exePath)
		return err
	}

	// Give the new process a moment to initialize before we exit
	time.Sleep(100 * time.Millisecond)
	os.Exit(0)

	return nil
}

// CleanupOld removes the .old backup file if UPDATER_CLEANUP_OLD env var is set
func CleanupOld() {
	if os.Getenv("UPDATER_CLEANUP_OLD") != "1" {
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		return
	}

	oldExe := exePath + ".old"
	_ = os.Remove(oldExe)
}
