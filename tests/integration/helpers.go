package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacemangaming/next-launcher/internal/channel"
	"github.com/spacemangaming/next-launcher/internal/github"
	"github.com/spacemangaming/next-launcher/internal/manifest"
	"github.com/spacemangaming/next-launcher/internal/paths"
)

// TestEnvironment represents a complete test environment
type TestEnvironment struct {
	T              *testing.T
	BaseDir        string
	GitHubServer   *httptest.Server
	ManifestMgr  *manifest.Manager
	ChannelMgr   ChannelManager
	GitHubClient *github.Client
	Cleanup      func()
}

// ChannelManager wraps channel operations for testing
type ChannelManager struct {
	baseDir string
}

// Save saves a channel
func (c *ChannelManager) Save(ch string) error {
	return channel.Save(c.baseDir, ch)
}

// Load loads the current channel
func (c *ChannelManager) Load() (string, error) {
	return channel.Load(c.baseDir)
}

// SetupTestEnvironment creates a complete test environment
func SetupTestEnvironment(t *testing.T) *TestEnvironment {
	t.Helper()

	// Create temporary directory
	baseDir := t.TempDir()

	// Create mock GitHub server
	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Default 404 response
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))

	// Create managers
	manifestMgr := manifest.NewManager(manifest.Config{
		ManifestFile: ".manifest",
		WorldsDir:    "worlds",
		WorldFileExt: ".mcl",
		QuietFlag:    true,
	})

	channelMgr := ChannelManager{baseDir: baseDir}

	// Create GitHub client pointing to mock server
	// Note: We can't easily redirect the client to mock server without refactoring
	// For now, create client but tests should use mock server directly
	githubClient := github.NewClient("testowner", "testrepo", &http.Client{})

	env := &TestEnvironment{
		T:            t,
		BaseDir:      baseDir,
		GitHubServer: githubServer,
		ManifestMgr:  manifestMgr,
		ChannelMgr:   channelMgr,
		GitHubClient: githubClient,
		Cleanup: func() {
			githubServer.Close()
		},
	}

	return env
}

// CreateFile creates a file with content in the test environment
func (e *TestEnvironment) CreateFile(relativePath, content string) error {
	e.T.Helper()

	fullPath := filepath.Join(e.BaseDir, relativePath)
	dir := filepath.Dir(fullPath)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	return os.WriteFile(fullPath, []byte(content), 0644)
}

// ReadFile reads a file from the test environment
func (e *TestEnvironment) ReadFile(relativePath string) (string, error) {
	e.T.Helper()

	fullPath := filepath.Join(e.BaseDir, relativePath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// FileExists checks if a file exists in the test environment
func (e *TestEnvironment) FileExists(relativePath string) bool {
	e.T.Helper()

	fullPath := filepath.Join(e.BaseDir, relativePath)
	_, err := os.Stat(fullPath)
	return err == nil
}

// CreateManifest creates a manifest file with the given entries
func (e *TestEnvironment) CreateManifest(files map[string]manifest.FileInfo) error {
	e.T.Helper()

	data, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	return e.CreateFile(".manifest", string(data))
}

// LoadManifest loads the current manifest
func (e *TestEnvironment) LoadManifest() (map[string]manifest.FileInfo, error) {
	e.T.Helper()

	// Temporarily change directory
	originalDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	defer os.Chdir(originalDir)

	os.Chdir(e.BaseDir)
	return e.ManifestMgr.LoadLocal()
}

// SetupMockGitHubTree configures the mock server to return a tree
func (e *TestEnvironment) SetupMockGitHubTree(ref string, tree []github.TreeItem) {
	e.T.Helper()

	// Update the server handler to respond to tree requests
	e.GitHubServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match tree request pattern: /repos/owner/repo/git/trees/{ref}?recursive=1
		if r.URL.Query().Get("recursive") == "1" {
			response := github.Tree{
				SHA:  "tree-sha-" + ref,
				Tree: tree,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
			return
		}

		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	})
}

// SetupMockCommit configures the mock server to return a commit
func (e *TestEnvironment) SetupMockCommit(ref string, commit github.Commit) {
	e.T.Helper()

	e.GitHubServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match commit request pattern
		if r.URL.Path == fmt.Sprintf("/repos/testowner/testrepo/commits/%s", ref) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(commit)
			return
		}

		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	})
}

// AssertFileContent asserts that a file has specific content
func (e *TestEnvironment) AssertFileContent(relativePath, expectedContent string) {
	e.T.Helper()

	content, err := e.ReadFile(relativePath)
	if err != nil {
		e.T.Fatalf("failed to read file %s: %v", relativePath, err)
	}

	if content != expectedContent {
		e.T.Errorf("file %s content = %q, want %q", relativePath, content, expectedContent)
	}
}

// AssertFileExists asserts that a file exists
func (e *TestEnvironment) AssertFileExists(relativePath string) {
	e.T.Helper()

	if !e.FileExists(relativePath) {
		e.T.Errorf("file should exist: %s", relativePath)
	}
}

// AssertFileNotExists asserts that a file does not exist
func (e *TestEnvironment) AssertFileNotExists(relativePath string) {
	e.T.Helper()

	if e.FileExists(relativePath) {
		e.T.Errorf("file should not exist: %s", relativePath)
	}
}

// BuildManifestFromFiles creates a manifest from actual files in baseDir
func (e *TestEnvironment) BuildManifestFromFiles(ref string) (map[string]manifest.FileInfo, error) {
	e.T.Helper()

	normalize := func(p string) string {
		return paths.Normalize(p)
	}

	getRawURL := func(r, p string) string {
		return fmt.Sprintf("https://raw.githubusercontent.com/testowner/testrepo/%s/%s", r, p)
	}

	// Scan directory and build tree items
	var treeItems []manifest.TreeItem
	err := filepath.Walk(e.BaseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and hidden files
		if info.IsDir() || info.Name()[0] == '.' {
			return nil
		}

		// Get relative path
		relPath, err := filepath.Rel(e.BaseDir, path)
		if err != nil {
			return err
		}

		treeItems = append(treeItems, manifest.TreeItem{
			Path: relPath,
			Type: "blob",
			SHA:  "fake-sha-" + relPath,
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return e.ManifestMgr.BuildFromTree(ref, treeItems, normalize, getRawURL)
}
