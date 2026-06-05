package main

//go:generate go run github.com/akavel/rsrc@latest -manifest app.manifest -o rsrc.syso

import (
	"archive/zip"
	"crypto/sha1"
	"bufio"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cavaliergopher/grab/v3"
	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"

	"github.com/spacemangaming/next-launcher/internal/audio"
	"github.com/spacemangaming/next-launcher/internal/changelog"
	"github.com/spacemangaming/next-launcher/internal/channel"
	"github.com/spacemangaming/next-launcher/internal/embedded"
	"github.com/spacemangaming/next-launcher/internal/console"
	"github.com/spacemangaming/next-launcher/internal/github"
	"github.com/spacemangaming/next-launcher/internal/install"
	"github.com/spacemangaming/next-launcher/internal/manifest"
	"github.com/spacemangaming/next-launcher/internal/paths"
	"github.com/spacemangaming/next-launcher/internal/process"
	"github.com/spacemangaming/next-launcher/internal/prompt"
	"github.com/spacemangaming/next-launcher/internal/selfupdate"
	"github.com/spacemangaming/next-launcher/internal/version"
)

// ============================================================================
// FUNCTION INDEX
// ============================================================================
// This file contains the updater application logic (~3400 lines).
// Core functionality is delegated to internal packages:
//   - internal/audio: Sound playback
//   - internal/channel: Update channel persistence
//   - internal/console: Console I/O and title
//   - internal/github: GitHub API client
//   - internal/install: Installation, world files, batch scripts
//   - internal/manifest: Manifest management
//   - internal/paths: Path normalization and exclusions
//   - internal/process: Process detection
//   - internal/selfupdate: Self-update mechanism
//   - internal/version: Version parsing and management
//
// Use this index to navigate to major sections:
//
// 1. AUDIO/SOUND SYSTEM (wrappers for internal/audio)
//
// 2. CONSOLE/UI (wrappers for internal/console)
//    - initConsole, waitForUser, confirmAction
//
// 3. GITHUB API (wrappers for internal/github)
//    - getLatestCommit, compareCommits, getLastCommitDate, validateChannelSwitch,
//      getLatestTag, getZipURLForChannel, getGitHubTree, getRawURLForTag
//
// 4. MANIFEST MANAGEMENT
//    - loadRemoteManifest, saveManifest
//
// 5. UPDATE OPERATIONS
//    - getPendingUpdates, printCheckOutput, performUpdates, downloadFile,
//      downloadAndExtractZip, downloadZipAndExtract
//
// 6. INSTALLATION
//    - handleInstallation, copyUpdaterToInstallation
//
// 7. PROCESS DETECTION (uses internal/process)
//    - isProxianiRunning, isMUDMixerRunning, isMUSHClientRunning
//
// 8. WORLD FILE UPDATES (uses internal/install)
//    - updateWorldFile, updateWorldFileForProxiani, updateWorldFileForMUDMixer
//
// 9. VERSION MANAGEMENT (uses internal/version)
//    - getLatestVersion, getLocalVersion
//
// 10. CHANNEL MANAGEMENT (uses internal/channel)
//     - saveChannel, loadChannel, isValidChannel, promptForChannel,
//       promptForBranch
//
// 11. INSTALLATION DETECTION (uses internal/install)
//     - isInstalled, hasWorldFilesInCurrentDir, detectToastushInstallation,
//       getDesktopPath, checkDesktopShortcut, getShortcutTarget
//
// 12. FILE OPERATIONS (uses internal/paths)
//     - loadExcludes, moveToOldFolder, cleanOldFolder, hashFile
//
// 13. PROMPTING/MENUS
//     - promptForInstallFolder, promptInstallationMenu
//
// 14. CHANGELOG/RELEASE NOTES
//     - buildChangelog, showChangelog
//
// 15. MIGRATION
//     - handleToastushMigration
//
// 16. MISCELLANEOUS
//     - needsMUSHClientRestart, launchMUSHClient, fatalError,
//       createUpdaterExcludes, writeUpdateSuccess
//
// 17. MAIN
//     - main (primary entry point)
// ============================================================================

//go:embed sounds/error.wav
var errorSound []byte

//go:embed sounds/downloading.wav
var downloadingSound []byte

//go:embed sounds/installing.wav
var installingSound []byte

//go:embed sounds/success.wav
var successSound []byte

//go:embed sounds/start.wav
var startSound []byte

//go:embed sounds/proxiani.wav
var proxianiSound []byte

//go:embed sounds/up_to_date.wav
var upToDateSound []byte

//go:embed sounds/select.wav
var selectSound []byte

var (
	_ embed.FS // Ensure embed package is recognized by compiler
)

// ============================================================================
// SECTION 1: AUDIO/SOUND SYSTEM (delegated to internal/audio)
// ============================================================================

// Wrapper functions for audio package - these exist so we don't have to
// update every call site in the codebase

func playSound(soundData []byte) {
	audio.Play(soundData)
}

func stopAllSounds() {
	audio.StopAll()
}

func playSoundAsync(soundData []byte, volumeDB float64) {
	audio.PlayAsync(soundData, volumeDB)
}

func playSoundAsyncLoop(soundData []byte, volumeDB float64, loop bool) {
	audio.PlayAsyncLoop(soundData, volumeDB, loop)
}

func playSoundWithDucking(soundData []byte, foregroundVolumeDB float64) {
	audio.PlayWithDucking(soundData, foregroundVolumeDB)
}

// soundAdapter implements prompt.SoundPlayer
type soundAdapter struct{}

func (s soundAdapter) Play(name string) {
	switch name {
	case "select":
		playSound(selectSound)
	case "success":
		playSound(successSound)
	case "error":
		playSound(errorSound)
	}
}

func (s soundAdapter) PlayAsync(name string) {
	switch name {
	case "select":
		playSoundAsync(selectSound, 0.0)
	case "success":
		playSoundAsync(successSound, 0.0)
	case "error":
		playSoundAsync(errorSound, 0.0)
	}
}

// promptConfig returns the prompt configuration for the current state
func promptConfig() prompt.Config {
	return prompt.Config{
		NonInteractive:   nonInteractive,
		Sound:            soundAdapter{},
		GetConsoleWindow: console.GetWindow,
	}
}

// ============================================================================
// SECTION 2: CONSOLE/UI (delegated to internal/console)
// ============================================================================

func initConsole() bool {
	return console.Attach()
}

// appVersion is set via linker flags: -ldflags "-X main.appVersion=1.3.2"
var appVersion = "dev"

const (
	githubOwner  = "spacemangaming"
	githubRepo   = "miriani-next"
	manifestFile = ".manifest"
	versionFile  = "version.json"
	excludesFile = ".updater-excludes"
	channelFile  = ".update-channel"
	zipThreshold = 30
	fileWorkers  = 6
	title        = "Miriani"

	// World file and directory names
	worldFileName = "miriani.mcl"
	worldsDir     = "worlds"
	worldFileExt  = ".mcl"

	// Server addresses
	defaultServer = "toastsoft.net"
	localServer   = "localhost"

	// Port numbers for Proxiani and MUDMixer
	proxianiPort = "1234"
	mudMixerPort = "7788"

	// Default Toastush miriani.mcl SHA1 hash (unmodified version)
	defaultToastushMCLHash = "57b5a6a2ace40a151fe3f1e1eddd029189ff9097"

	// Windows process creation flags
	DETACHED_PROCESS = 0x00000008
)

var (
	// baseURL is dynamically constructed based on channel
	baseURL string
	// httpClient with connection pooling and timeouts
	httpClient *http.Client
	// ghClient is the GitHub API client
	ghClient *github.Client
	// manifestManager handles manifest operations
	manifestManager *manifest.Manager
)

var (
	quietFlag               bool
	verboseFlag             bool
	versionFlag             bool
	channelFlag             string
	generateManifest        bool
	nonInteractive          bool
	switchChannel           string
	switchChannelSubcommand bool
	channelExplicitlySet    bool
	allowRestartFlag        bool
	selfUpdateCheckFlag     bool
	subcommand              string // Current subcommand being executed
	shortcutNameFlag        string
)

// ErrUserCancelled is returned when the user cancels an operation
var ErrUserCancelled = fmt.Errorf("operation cancelled by user")

// Version is an alias for version.Version for backwards compatibility
type Version = version.Version

// versionString returns the version in semantic format as a string
func versionString(v Version) string {
	ver := fmt.Sprintf("%d.%d.%02d", v.Major, v.Minor, v.Patch)
	if v.Commit != "" {
		ver += fmt.Sprintf("+%s", v.Commit[:7])
	}
	return ver
}

// ============================================================================
// SECTION 3: GITHUB API
// ============================================================================

func getLatestCommit(ref string) (*github.Commit, error) {
	return ghClient.GetLatestCommit(ref)
}

func compareCommits(base, head string) (*github.Comparison, error) {
	return ghClient.CompareCommits(base, head)
}

func getLastCommitDate(ref string) (string, error) {
	dateStr, err := ghClient.GetLastCommitDate(ref)
	if err != nil {
		return "", err
	}

	// Parse and format the date
	t, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		return dateStr, nil // Return raw if parsing fails
	}

	return t.Format("Jan 2, 2006"), nil
}

// validateChannelSwitch validates switching from one channel to another
// Returns an error if the switch would be a downgrade, or nil if safe
func validateChannelSwitch(fromChannel, toChannel string) error {
	if fromChannel == "" || fromChannel == toChannel {
		return nil // No switch
	}

	// Switching from dev/experimental to stable
	if toChannel == "stable" && (fromChannel == "dev" || (fromChannel != "stable" && fromChannel != "dev")) {
		if verboseFlag || nonInteractive {
			fmt.Println("Checking if stable is ahead of your current version...")
		}

		latestTag, err := getLatestTag()
		if err != nil {
			return fmt.Errorf("failed to get latest stable tag: %w", err)
		}

		compareBranch := "main"
		if fromChannel != "dev" {
			compareBranch = fromChannel
		}

		comparison, err := compareCommits(compareBranch, latestTag)
		if err != nil {
			return fmt.Errorf("failed to compare commits: %w", err)
		}

		if comparison.BehindBy > 0 {
			fmt.Printf("\nCannot switch to stable - it is older than your current version.\n")
			fmt.Printf("Stable (%s) is %d commits behind %s.\n", latestTag, comparison.BehindBy, fromChannel)
			fmt.Println("\nThis would downgrade your installation, which could cause issues.")
			fmt.Println("\nPlease wait for the next stable release before switching.")
			playSoundAsync(errorSound, 0.0)
			return fmt.Errorf("stable is behind %s, refusing downgrade", fromChannel)
		}

		if comparison.AheadBy > 0 {
			if !quietFlag {
				fmt.Printf("Stable (%s) is %d commits ahead of %s. Safe to switch.\n", latestTag, comparison.AheadBy, fromChannel)
			}
		} else {
			if !quietFlag {
				fmt.Printf("Stable (%s) is at the same commit as %s. Safe to switch.\n", latestTag, fromChannel)
			}
		}
		return nil
	}

	// Switching from stable to dev
	if toChannel == "dev" && fromChannel == "stable" {
		if !quietFlag {
			fmt.Println("Checking if dev is ahead of your current stable version...")
		}

		latestTag, err := getLatestTag()
		if err != nil {
			if !quietFlag {
				fmt.Println("Warning: couldn't check stable version for comparison")
			}
			return nil
		}

		comparison, err := compareCommits("main", latestTag)
		if err != nil {
			if !quietFlag {
				fmt.Println("Warning: couldn't compare dev to stable")
			}
			return nil
		}

		// BehindBy = how many commits the tag is behind main = dev is AHEAD
		// AheadBy = how many commits the tag is ahead of main = dev is BEHIND
		if comparison.AheadBy > 0 {
			// Dev is behind stable!
			fmt.Printf("\nWARNING: Dev (main) is %d commits BEHIND stable (%s).\n", comparison.AheadBy, latestTag)
			fmt.Println("Switching to dev would be a DOWNGRADE.")
			if !confirmAction("Switch to older dev version anyway?") {
				return fmt.Errorf("user cancelled downgrade to dev")
			}
		} else if !quietFlag && comparison.BehindBy > 0 {
			fmt.Printf("Dev is ahead of stable (%s) by %d commits. Safe to switch.\n", latestTag, comparison.BehindBy)
		}
		return nil
	}

	// Switching from experimental to dev/stable?
	if fromChannel != "stable" && fromChannel != "dev" && (toChannel == "dev" || toChannel == "stable") {
		if !quietFlag {
			fmt.Printf("Checking if %s is ahead of your current %s branch...\n", toChannel, fromChannel)
		}

		var targetRef string
		if toChannel == "stable" {
			tag, err := getLatestTag()
			if err != nil {
				return fmt.Errorf("failed to get latest stable tag: %w", err)
			}
			targetRef = tag
		} else {
			targetRef = "main"
		}

		comparison, err := compareCommits(targetRef, fromChannel)
		if err != nil {
			// Non-fatal, just warn
			if !quietFlag {
				fmt.Printf("Warning: couldn't compare %s to %s\n", toChannel, fromChannel)
			}
			return nil
		}

		if comparison.BehindBy > 0 {
			// Target is behind experimental!
			fmt.Printf("\nWARNING: %s is %d commits BEHIND %s.\n", toChannel, comparison.BehindBy, fromChannel)
			fmt.Println("Switching would be a DOWNGRADE.")

			if !confirmAction(fmt.Sprintf("Switch to older %s version anyway?", toChannel)) {
				return fmt.Errorf("user cancelled downgrade to %s", toChannel)
			}
		} else if !quietFlag && comparison.AheadBy > 0 {
			fmt.Printf("%s is %d commits ahead of %s. Safe to switch.\n", toChannel, comparison.AheadBy, fromChannel)
		}
		return nil
	}

	return nil
}

func getLatestTag() (string, error) {
	return ghClient.GetLatestTag()
}

func getZipURLForChannel() (string, error) {
	if channelFlag == "stable" {
		tag, err := getLatestTag()
		if err != nil {
			return "", fmt.Errorf("failed to get latest tag: %w", err)
		}
		return fmt.Sprintf("%s/archive/refs/tags/%s.zip", baseURL, tag), nil
	} else if channelFlag == "dev" {
		return fmt.Sprintf("%s/archive/refs/heads/main.zip", baseURL), nil
	}
	// For custom branches
	return fmt.Sprintf("%s/archive/refs/heads/%s.zip", baseURL, channelFlag), nil
}

func getGitHubTree(ref string) (*github.Tree, error) {
	return ghClient.GetTree(ref)
}

func getRawURLForTag(tag string, path string) string {
	return ghClient.GetRawURL(tag, path)
}

// ============================================================================
// SECTION 12: FILE OPERATIONS
// ============================================================================

type UpdateResult struct {
	Result       string   `json:"result"`                  // "success" or "failure"
	Message      string   `json:"message,omitempty"`       // Error message if failure
	Version      string   `json:"version,omitempty"`       // Full version string if success
	FilesAdded   []string `json:"files_added,omitempty"`   // Array of added/updated file paths
	FilesDeleted []string `json:"files_deleted,omitempty"` // Array of deleted file paths
	Restarted    bool     `json:"restarted"`               // Whether MUSHclient was restarted
}

func writeUpdateSuccess(updates []manifest.FileInfo, deletedFiles []string, wasRestarted bool) error {
	baseDir, err := os.Getwd()
	if err != nil {
		return err
	}

	// Get the current version
	versionStr := "unknown"
	if latestVer, err := getLatestVersion(); err == nil {
		versionStr = latestVer.String()
	}

	// Build lists of added/updated and deleted file paths
	filesAdded := make([]string, 0, len(updates))
	for _, update := range updates {
		filesAdded = append(filesAdded, update.Name)
	}

	result := UpdateResult{
		Result:       "success",
		Version:      versionStr,
		FilesAdded:   filesAdded,
		FilesDeleted: deletedFiles,
		Restarted:    wasRestarted,
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal update result: %w", err)
	}

	resultPath := filepath.Join(baseDir, ".update-result")
	return os.WriteFile(resultPath, append(jsonData, '\n'), 0644)
}

// ============================================================================
// SECTION 17: MAIN
// ============================================================================

func main() {
	// Global panic handler to prevent path leakage in error messages
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\nAn unexpected error occurred: %v\n", r)
			fmt.Fprintln(os.Stderr, "Please report this issue to the developers.")
			playSound(errorSound)
			if !nonInteractive {
				waitForUser("\nPress Enter to exit...")
			}
			os.Exit(1)
		}
	}()

	// Configure log package to not include file paths
	log.SetFlags(0)

	// Clean up old updater binary if we just self-updated
	selfupdate.CleanupOld()

	// Normalize double-dash flags to single-dash (Go's flag package uses single dash)
	// This allows users to use --channel instead of -channel
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--") && !strings.HasPrefix(arg, "---") {
			os.Args[i] = arg[1:] // Remove one dash
		}
	}

	// Check for subcommands before parsing flags
	var subcommandArgs []string
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		subcommand = os.Args[1]
		subcommandArgs = os.Args[2:]
	}

	// Parse flags FIRST so we know if we're in non-interactive mode
	defaultChannel := "stable"
	flag.StringVar(&channelFlag, "channel", defaultChannel, "Update channel: stable or dev")
	flag.BoolVar(&quietFlag, "quiet", false, "Suppress output")
	flag.BoolVar(&verboseFlag, "verbose", false, "Show detailed output including every file")
	flag.BoolVar(&versionFlag, "version", false, "Show updater version and exit")
	flag.BoolVar(&generateManifest, "generate-manifest", false, "Generate manifest file for current directory")
	flag.BoolVar(&nonInteractive, "non-interactive", false, "Non-interactive mode: log to file, no prompts, write .update-success")
	flag.BoolVar(&allowRestartFlag, "allow-restart", false, "Allow restart in non-interactive mode (use with -non-interactive)")
	flag.BoolVar(&selfUpdateCheckFlag, "self-update-check", false, "Internal: Check for updater self-update (spawned in background)")
	flag.StringVar(&shortcutNameFlag, "shortcut-name", "Miriani-Next", "Name of the created desktop shortcut")

	// Only parse flags if not using subcommand syntax
	if subcommand == "" {
		flag.Parse()
	} else {
		// Parse subcommand args - let flag package handle flag/value separation
		flag.CommandLine.Parse(subcommandArgs)
	}

	// Initialize console and audio packages
	console.Init(quietFlag)
	audio.Init(quietFlag, verboseFlag, func(format string, args ...interface{}) {
		log.Printf(format, args...)
	})

	// Attach to or create console for output
	initConsole()

	console.SetTitle(title)
	// Clean up old updater binary if this is a post-update restart
	if os.Getenv("UPDATER_CLEANUP_OLD") == "1" {
		if exePath, err := os.Executable(); err == nil {
			oldExe := exePath + ".old"
			// Retry removal a few times with delays (Windows might have file locked)
			for i := 0; i < 3; i++ {
				if err := os.Remove(oldExe); err == nil {
					break
				}
				if i < 2 {
					time.Sleep(500 * time.Millisecond)
				}
			}
		}
		// Clear the environment variable so it doesn't persist
		os.Unsetenv("UPDATER_CLEANUP_OLD")
	}

	// Handle subcommands
	switch subcommand {
	case "check":
		// Check subcommand - handled after initialization
	case "switch":
		// Get channel from first remaining arg after flags
		if len(flag.Args()) > 0 {
			switchChannel = flag.Args()[0]
		} else {
			switchChannel = "" // Will prompt interactively
		}
		switchChannelSubcommand = true
	case "":
		// No subcommand, continue normally
	default:
		fmt.Printf("Unknown subcommand: %s\n", subcommand)
		fmt.Println("\nAvailable subcommands:")
		fmt.Println("  check                    Check for updates only")
		fmt.Println("  switch [stable|dev]      Switch update channel (prompts if no channel specified)")
		fmt.Println("\nOr run without subcommand to update")
		os.Exit(1)
	}

	// Check if channel was explicitly set
	channelExplicitlySet = false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "channel" {
			channelExplicitlySet = true
		}
	})

	// Check for environment variable to allow restart
	if os.Getenv("UPDATER_ALLOW_RESTART") == "1" {
		allowRestartFlag = true
	}

	// Initialize HTTP client with connection pooling and timeouts (needed early for self-update)
	httpClient = &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // Required for GitHub archive downloads (already compressed)
		},
	}

	// Initialize GitHub API client
	ghClient = github.NewClient(githubOwner, githubRepo, httpClient)

	// Initialize manifest manager
	manifestManager = manifest.NewManager(manifest.Config{
		ManifestFile: manifestFile,
		WorldsDir:    worldsDir,
		WorldFileExt: worldFileExt,
		ChannelFlag:  channelFlag,
		QuietFlag:    quietFlag,
		VerboseFlag:  verboseFlag,
	})

	// Load channel before check command (so check uses correct channel)
	if !channelExplicitlySet {
		if loadedChannel, err := loadChannel(); err == nil {
			channelFlag = loadedChannel
			if !quietFlag && verboseFlag {
				fmt.Printf("Using saved channel: %s\n", channelFlag)
			}
		}
	}

	// Validate channel BEFORE check command (so invalid channels get fixed)
	if channelFlag != "stable" && channelFlag != "dev" {
		// Check if it's a valid branch
		if !isValidChannel(channelFlag) {
			// Branch doesn't exist, fall back to dev
			oldChannel := channelFlag
			channelFlag = "dev"

			// Save the fallback channel immediately
			if err := saveChannel(channelFlag); err != nil {
				fmt.Printf("Warning: failed to save channel preference: %v\n", err)
			} else {
				// Only print success message if save worked
				if !quietFlag {
					fmt.Printf("\nThe experimental branch '%s' no longer exists!\n", oldChannel)
					fmt.Printf("Automatically switching you to the 'dev' channel.\n")
					fmt.Printf("You'll now receive updates from the main development branch.\n\n")
				}
			}
		} else {
			// It's a custom branch
			if !quietFlag && !verboseFlag {
				fmt.Printf("WARNING: Using experimental branch: %s\n", channelFlag)
			}
		}
	}

	// Handle check subcommand early (after httpClient init and channel load)
	if subcommand == "check" {
		updates, deletedFiles, err := getPendingUpdates()
		if err != nil {
			fatalError("Error checking updates: %v", err)
		}
		printCheckOutput(updates, deletedFiles)

		// Spawn detached self-update check before exiting
		exePath, err := os.Executable()
		if err == nil {
			cmd := exec.Command(exePath, "--self-update-check")
			// Detach completely - don't inherit handles
			cmd.SysProcAttr = &syscall.SysProcAttr{
				CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS,
			}
			// Close all standard handles so process doesn't inherit them
			cmd.Stdin = nil
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Start(); err == nil {
				cmd.Process.Release() // Release the process handle immediately
			}
		}

		return
	}

	// If version flag is set, print version and exit
	if versionFlag {
		fmt.Printf("Miriani-Next Updater v%s\n", appVersion)
		return
	}

	// If self-update check flag is set, wait briefly then check for updates
	if selfUpdateCheckFlag {
		time.Sleep(500 * time.Millisecond) // Wait for parent process to exit
		_ = selfupdate.Check(selfupdate.DefaultConfig(appVersion))
		return
	}

	// If generating manifest, do that and exit
	if generateManifest {
		if err := saveManifest(); err != nil {
			fatalError("Failed to generate manifest: %v", err)
		}
		return
	}

	if switchChannelSubcommand {
		var newChannel string

		// If a channel value was provided, validate and use it
		if switchChannel == "stable" || switchChannel == "dev" {
			newChannel = switchChannel
			fmt.Printf("Switching to %s channel...\n", newChannel)
		} else if switchChannel == "" {
			// No value provided
			if nonInteractive {
				// In non-interactive mode, require channel to be specified
				fmt.Println("Error: Channel must be specified in non-interactive mode.")
				fmt.Println("Usage: updater switch <stable|dev>")
				os.Exit(1)
			}
			// Prompt interactively
			newChannel = promptForChannel()
		} else {
			// Invalid value provided
			fmt.Printf("Error: Invalid channel '%s'. Must be 'stable' or 'dev'.\n", switchChannel)
			playSoundAsync(errorSound, 0.0)
			if !nonInteractive {
				waitForUser("\nPress Enter to exit...")
			}
			os.Exit(1)
		}

		// Load current channel and validate the switch
		currentChannel, _ := loadChannel()
		if err := validateChannelSwitch(currentChannel, newChannel); err != nil {
			if !nonInteractive {
				waitForUser("\nPress Enter to exit...")
			}
			os.Exit(1)
		}
		if err := saveChannel(newChannel); err != nil {
			fatalError("Failed to save channel preference: %v", err)
		}
		fmt.Printf("\nUpdate channel changed to: %s\n", newChannel)
		fmt.Println("Run the updater again to update using the new channel.")

		if !nonInteractive {
			waitForUser("\nPress Enter to exit...")
		}
		return

	}

	// If no channel flag was explicitly set, try to load saved channel
	var savedChannel string
	if !channelExplicitlySet {
		if loadedChannel, err := loadChannel(); err == nil {
			savedChannel = loadedChannel
			channelFlag = savedChannel
			if !quietFlag && verboseFlag {
				fmt.Printf("Using saved channel: %s\n", channelFlag)
			}
		}
		// If no saved channel and not installed, prompt for channel during installation
		// (handled in handleInstallation)
	} else {
		// Channel was explicitly set, remember what was saved before
		savedChannel, _ = loadChannel()
	}

	// Set baseURL
	baseURL = fmt.Sprintf("https://github.com/%s/%s", githubOwner, githubRepo)

	if verboseFlag && !quietFlag {
		if channelFlag == "stable" {
			if tag, err := getLatestTag(); err == nil {
				fmt.Printf("Latest available: %s\n", tag)
			}
		} else {

			if commit, err := getLatestCommit("main"); err == nil {
				fmt.Printf("Latest available: %s (commit %s)\n",
					commit.Commit.Committer.Date, commit.SHA[:7])
			}
		}
	}

	if !isInstalled() {
		// Not installed in current directory
		usr, _ := os.UserHomeDir()
		expectedInstallDir := filepath.Join(usr, "Documents", "Miriani-Next")

		// Check if installation exists in expected location
		existingInstallFound := false
		if _, err := os.Stat(filepath.Join(expectedInstallDir, "MUSHclient.exe")); err == nil {
			existingInstallFound = true
		}

		// Check for a Toastush installation
		toastushPath := detectToastushInstallation()

		playSoundAsync(startSound, 0.0)

		var choice string
		if !nonInteractive {
			choice = promptInstallationMenu(existingInstallFound, expectedInstallDir, toastushPath)
		} else {
			// Non-interactive mode: auto-detect behavior
			if toastushPath != "" {
				choice = "3" // Migrate from Toastush
			} else if existingInstallFound {
				choice = "2" // Install updater to existing installation
			} else {
				choice = "1" // Fresh install
			}
		}

		switch choice {
		case "1":
			// Full installation
			installDir, err := handleInstallation()
			if err != nil {
				// Check if user cancelled
				if err == ErrUserCancelled {
					fmt.Println("Exiting in 3 seconds...")
					time.Sleep(3 * time.Second)
					return
				}
				// Other errors are fatal
				fatalError("Installation failed: %v", err)
			}

			// Launch MUSHclient after successful installation

			// Give a moment for background sounds to finish
			time.Sleep(500 * time.Millisecond)

			// Play success sound (blocks until sound finishes)
			playSound(successSound)

			// Change to install directory and launch
			if err := os.Chdir(installDir); err != nil {
				if !quietFlag && verboseFlag {
					fmt.Printf("Warning: couldn't change to install directory: %v\n", err)
				}
			}

			// Try to launch MUSHclient
			if !quietFlag {
				fmt.Println("Attempting to launch MUSHclient...")
			}
			if err := launchMUSHClient(); err != nil {
				fmt.Printf("Failed to launch MUSHclient: %v\n", err)
				fmt.Printf("Working directory: %s\n", installDir)
				waitForUser("\nPress Enter to exit...")
				return
			}
			return

		case "2":
			// Install updater to existing installation
			installDir := expectedInstallDir

			// If we didn't auto-detect an installation, prompt for the directory
			if !existingInstallFound {
				if !nonInteractive {
					fmt.Println("\nLocate your existing Miriani-Next installation")
					selectedDir, err := promptForInstallFolder(expectedInstallDir)
					if err != nil {
						fmt.Printf("Error selecting folder: %v\n", err)
						waitForUser("\nPress Enter to exit...")
						return
					}
					installDir = selectedDir

					// Verify it's a valid installation
					if _, err := os.Stat(filepath.Join(installDir, "MUSHclient.exe")); os.IsNotExist(err) {
						fmt.Printf("\nMUSHclient.exe not found in: %s\n", installDir)
						fmt.Println("This doesn't appear to be a valid Miriani-Next installation.")
						playSound(errorSound)
						waitForUser("\nPress Enter to exit...")
						return
					}
				} else {
					// Non-interactive mode but no installation found
					console.Log("No existing installation found and cannot prompt for location in non-interactive mode")
					return
				}
			} else {
				// Auto-detected installation - confirm with user
				if !nonInteractive {
					fmt.Printf("\nFound existing installation at: %s\n", installDir)
					if !confirmAction("Install updater to this location?") {
						fmt.Println("\nLocate your Miriani-Next installation")
						selectedDir, err := promptForInstallFolder(expectedInstallDir)
						if err != nil {
							fmt.Printf("Error selecting folder: %v\n", err)
							waitForUser("\nPress Enter to exit...")
							return
						}
						installDir = selectedDir

						// Verify it's a valid installation
						if _, err := os.Stat(filepath.Join(installDir, "MUSHclient.exe")); os.IsNotExist(err) {
							fmt.Printf("\nMUSHclient.exe not found in: %s\n", installDir)
							fmt.Println("This doesn't appear to be a valid Miriani-Next installation.")
							playSound(errorSound)
							waitForUser("\nPress Enter to exit...")
							return
						}
					}
				}
			}

			// Check if updater already exists
			updaterInInstallDir := filepath.Join(installDir, "update.exe")
			if _, err := os.Stat(updaterInInstallDir); err == nil {
				fmt.Printf("\nUpdater already exists at: %s\n", installDir)
				fmt.Println("Please run the updater from that directory.")
				playSound(errorSound)
				waitForUser("\nPress Enter to exit...")
				return
			}

			// If no channel was explicitly set and no saved channel, prompt for selection
			if !channelExplicitlySet && !nonInteractive {
				if _, err := loadChannel(); err != nil {
					// No saved channel in existing install, prompt user
					channelFlag = promptForChannel()
				}
			}

			// Copy updater to installation
			if err := copyUpdaterToInstallation(installDir); err != nil {
				fmt.Printf("Error copying updater: %v\n", err)
				playSound(errorSound)
				waitForUser("\nPress Enter to exit...")
				return
			}

			// Check if manifest is missing and generate if needed
			manifestPath := filepath.Join(installDir, manifestFile)
			if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
				if !quietFlag {
					fmt.Println("Generating manifest...")
				}
				// Change to install directory to generate manifest
				if err := os.Chdir(installDir); err == nil {
					if err := saveManifest(); err != nil {
						fmt.Printf("Warning: failed to generate manifest: %v\n", err)
					} else if !quietFlag {
						fmt.Println("Manifest generated successfully!")
					}
				}
			}

			// Change to install directory
			originalDir, _ := os.Getwd()
			if err := os.Chdir(installDir); err != nil {
				fmt.Printf("Warning: failed to change to install directory: %v\n", err)
			}

			// Save channel preference
			if err := saveChannel(channelFlag); err != nil {
				fmt.Printf("Warning: failed to save channel preference: %v\n", err)
			}

			// Create .updater-excludes file to protect user configuration
			if err := createUpdaterExcludes(); err != nil {
				fmt.Printf("Warning: failed to create .updater-excludes: %v\n", err)
			} else if !quietFlag && verboseFlag {
				fmt.Println("Created .updater-excludes file to protect user configuration")
			}

			// Create channel switching batch files
			if err := install.CreateChannelSwitchBatchFiles(installDir); err != nil {
				fmt.Printf("Warning: failed to create channel switch batch files: %v\n", err)
			} else if !quietFlag {
				fmt.Println("Created channel switching batch files")
			}

			fmt.Printf("\nUpdater installed successfully to: %s\n", installDir)

			// Run the updater from the new location to get them up to date
			if !nonInteractive {
				fmt.Println("\nRunning updater to check for updates...")
				updaterPath := filepath.Join(installDir, "update.exe")
				cmd := exec.Command(updaterPath)
				cmd.Dir = installDir
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				cmd.Stdin = os.Stdin
				if err := cmd.Run(); err != nil {
					fmt.Printf("Warning: failed to run updater: %v\n", err)
					playSoundAsync(errorSound, 0.0)
					waitForUser("\nPress Enter to exit...")
				}
				return
			}

			// Restore original directory
			if originalDir != "" {
				os.Chdir(originalDir)
			}

			playSound(successSound)
			waitForUser("\nPress Enter to exit...")
			return

		case "3":
			// Migrate from Toastush
			if err := handleToastushMigration(toastushPath); err != nil {
				// Check if user cancelled
				if err == ErrUserCancelled {
					fmt.Println("Exiting in 5 seconds...")
					time.Sleep(5 * time.Second)
					return
				}
				// Other errors are fatal
				fatalError("Migration failed: %v", err)
			}

			// Get the new installation directory (after rename)
			installDir := filepath.Join(usr, "Documents", "Miriani-Next")
			if toastushPath != "" {
				// Use the renamed directory
				installDir = filepath.Join(filepath.Dir(toastushPath), "Miriani-Next")
			}

			// Give a moment for background sounds to finish
			time.Sleep(500 * time.Millisecond)

			// Play success sound
			playSound(successSound)

			// Change to install directory and launch
			if err := os.Chdir(installDir); err != nil {
				if !quietFlag && verboseFlag {
					fmt.Printf("Warning: couldn't change to install directory: %v\n", err)
				}
			}

			// Try to launch MUSHclient
			if !quietFlag {
				fmt.Println("Attempting to launch MUSHclient...")
			}
			if err := launchMUSHClient(); err != nil {
				fmt.Printf("Failed to launch MUSHclient: %v\n", err)
				fmt.Printf("Working directory: %s\n", installDir)
				waitForUser("\nPress Enter to exit...")
				return
			}
			return

		default:
			fmt.Println("Installation cancelled.")
			waitForUser("\nPress Enter to exit...")
			return
		}
	}

	if err := cleanOldFolder(); err != nil {
		if !quietFlag && verboseFlag {
			fmt.Printf("Warning: failed to clean .old directory: %v\n", err)
		}
	}

	// Check if we're switching channels and if it would be a downgrade
	if err := validateChannelSwitch(savedChannel, channelFlag); err != nil {
		waitForUser("\nPress Enter to exit...")
		return
	}

	updates, deletedFiles, err := getPendingUpdates()
	if err != nil {
		fatalError("Error checking updates: %v", err)
		waitForUser("Press enter to exit...\n")
	}

	if len(updates) == 0 && len(deletedFiles) == 0 {
		fmt.Println("Already up to date!")
		if !quietFlag {
			playSoundAsync(upToDateSound, 0.0)
		}

		// Spawn detached self-update check before exiting
		exePath, err := os.Executable()
		if err == nil {
			cmd := exec.Command(exePath, "--self-update-check")
			// Detach completely - don't inherit handles
			cmd.SysProcAttr = &syscall.SysProcAttr{
				CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS,
			}
			// Close all standard handles so process doesn't inherit them
			cmd.Stdin = nil
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Start(); err == nil {
				cmd.Process.Release() // Release the process handle immediately
			}
		}

		waitForUser("\nPress Enter to exit...")
		return
	}

	if !quietFlag && !nonInteractive {
		totalChanges := len(updates) + len(deletedFiles)
		fmt.Printf("\n%d files will be changed (%d updates, %d deletions).\n", totalChanges, len(updates), len(deletedFiles))
	}

	// Track whether we killed MUSHclient so we know to restart it later
	// Only set to true if we actually kill the process
	mushWasRunning := false
	restartRequired := needsMUSHClientRestart(updates)

	// In non-interactive mode, check if restart is required without allow-restart flag
	if nonInteractive && restartRequired && !allowRestartFlag {
		// Check if MUSHclient is running
		if isMUSHClientRunning() {
			fmt.Println("restart required")
			return
		}
	}

	if restartRequired && isMUSHClientRunning() {
		if nonInteractive {
			// In non-interactive mode with allow-restart, kill MUSHclient before updating
			if allowRestartFlag {
				console.Log("MUSHclient is running. Killing MUSHclient to proceed with update...")
				baseDir, _ := os.Getwd()
				if err := process.KillMUSHClient(baseDir); err != nil {
					console.Log("Error: failed to kill MUSHclient: %v", err)
					return
				}
				mushWasRunning = true
				console.Log("MUSHclient killed successfully. Proceeding with update...")
				playSoundAsync(successSound, 0.0)
				// Wait for process to fully terminate
				if !process.WaitForMUSHClientTermination(baseDir, 5*time.Second) {
					console.Log("Warning: MUSHclient may not have fully terminated")
				}
			} else {
				// This shouldn't happen since we checked above, but handle it anyway
				fmt.Println("restart required")
				return
			}
		} else {
			// In interactive mode, tell user to close it
			fmt.Println("\nMUSHclient is running and needs to be closed to update it.")
			fmt.Println("MUSHclient.exe needs to be updated, but it is currently running.")
			fmt.Println("Please close MUSHclient and run the updater again.")
			playSoundAsync(errorSound, 0.0)
			waitForUser("\nPress Enter to exit...")
			return
		}
	}

	// Ask for confirmation before updating
	if !confirmAction("Do you want to proceed with the update?") {
		fmt.Println("Update cancelled.")
		return
	}

	if err := performUpdates(updates); err != nil {
		fatalError("Error updating: %v", err)
	}

	// Perform deletions for files that are no longer in the manifest
	baseDir, err := os.Getwd()
	if err != nil {
		fatalError("Error getting working directory: %v", err)
	}
	for _, path := range deletedFiles {
		filePath := filepath.Join(baseDir, paths.Denormalize(path))
		if err := moveToOldFolder(filePath, path); err == nil {
			if !quietFlag && verboseFlag && !nonInteractive {
				fmt.Printf("Removed: %s (moved to .old/)\n", path)
			}
		}
	}

	// Save current version after successful update
	// This updates the local .current_version file to match what we just downloaded
	if latestVer, err := getLatestVersion(); err == nil {
		if versionData, err := json.MarshalIndent(latestVer, "", "  "); err == nil {
			os.WriteFile(versionFile, versionData, 0644)
		}
	}

	// Show changelog
	if (len(updates) > 0 || len(deletedFiles) > 0) && !quietFlag && !nonInteractive {
		showChangelog(updates, deletedFiles)
	}

	// After update, restart MUSHclient if we killed it
	if mushWasRunning {
		console.Log("Restarting MUSHclient...")
		if err := launchMUSHClient(); err != nil {
			console.Log("Warning: failed to restart MUSHclient: %v", err)
			if !quietFlag && !nonInteractive {
				fmt.Printf("Warning: failed to restart MUSHclient: %v\n", err)
			}
		} else {
			console.Log("MUSHclient restarted successfully.")
			if !quietFlag && !nonInteractive {
				fmt.Println("MUSHclient restarted.")
			}
		}
	}

	playSound(successSound)
	if !quietFlag && !nonInteractive {
		fmt.Println("\nUpdate complete!")
	}

	// Write .update-result file in non-interactive mode
	if nonInteractive {
		if err := writeUpdateSuccess(updates, deletedFiles, mushWasRunning); err != nil {
			console.Log("Warning: failed to write .update-result: %v", err)
		}
	}

	// Spawn detached background process for self-update check (non-blocking)
	// This allows main process to exit immediately while self-update happens in background
	exePath, err := os.Executable()
	if err == nil {
		cmd := exec.Command(exePath, "--self-update-check")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS,
		}
		cmd.Start() // Fire and forget - don't wait
	}
}

// ============================================================================
// SECTION 5: UPDATE OPERATIONS
// ============================================================================

func getPendingUpdates() ([]manifest.FileInfo, []string, error) {
	localManifest, err := manifestManager.LoadLocal()
	if err != nil {
		// If manifest is missing or corrupted but we're in an installation directory, auto-generate it from local files
		if hasWorldFilesInCurrentDir() {
			if errors.Is(err, os.ErrNotExist) {
				if !quietFlag {
					fmt.Println("Manifest missing. Generating manifest from local files...")
				}
			} else {
				if !quietFlag {
					fmt.Printf("Manifest corrupted (%v). Regenerating from local files...\n", err)
				}
			}
			if err := saveManifest(); err != nil {
				return nil, nil, fmt.Errorf("failed to generate local manifest: %w", err)
			}
			// Try loading again after generation
			localManifest, err = manifestManager.LoadLocal()
			if err != nil {
				return nil, nil, err
			}
		} else {
			return nil, nil, err
		}
	}

	remoteManifest, err := loadRemoteManifest()
	if err != nil {
		return nil, nil, err
	}
	excludes := loadExcludes()

	// Normalize both manifests once for efficient comparison
	normalizedLocal := make(map[string]manifest.FileInfo, len(localManifest))
	for path, info := range localManifest {
		normalizedLocal[paths.Normalize(path)] = info
	}

	normalizedRemote := make(map[string]manifest.FileInfo, len(remoteManifest))
	for path, info := range remoteManifest {
		normalized := paths.Normalize(path)
		// Skip excluded files during normalization
		if paths.MatchesExclusion(normalized, excludes) {
			if !quietFlag && verboseFlag {
				fmt.Printf("Skipping excluded file: %s\n", normalized)
			}
			continue
		}
		normalizedRemote[normalized] = info
	}

	// Find updates: files in remote that are new or changed
	var updates []manifest.FileInfo
	for path, remote := range normalizedRemote {
		if local, exists := normalizedLocal[path]; !exists || local.Hash != remote.Hash {
			updates = append(updates, remote)
		}
	}

	// Find deletions: files in local but not in remote
	var deletedFiles []string
	if !quietFlag && verboseFlag {
		fmt.Println("Checking for removed files...")
	}
	for path := range normalizedLocal {
		if _, exists := normalizedRemote[path]; !exists {
			deletedFiles = append(deletedFiles, path)
		}
	}

	return updates, deletedFiles, nil
}

// printCheckOutput shows what updates are available (either human-readable or machine format)
func printCheckOutput(updates []manifest.FileInfo, deletedFiles []string) {
	hasUpdates := len(updates) > 0 || len(deletedFiles) > 0
	totalChanges := len(updates) + len(deletedFiles)
	restartRequired := needsMUSHClientRestart(updates)

	// Get version information
	latestVer, err := getLatestVersion()
	localVer, localErr := getLocalVersion()

	if nonInteractive {
		if !isInstalled() {
			fmt.Println("Update available: Unknown")
			fmt.Println("Status: Not installed")
		} else if !isValidChannel(channelFlag) {
			fmt.Println("Status: channel invalid")
		} else if hasUpdates {
			fmt.Println("Update available: Yes")
			if err == nil {
				fmt.Printf("Version: %s\n", latestVer.String())
			}
			if localErr == nil {
				fmt.Printf("Current version: %s\n", localVer.String())
			}
			if restartRequired {
				fmt.Println("Restart required: Yes")
			} else {
				fmt.Println("Restart required: No")
			}
			fmt.Printf("Changes: %d\n", totalChanges)
			fmt.Printf("Updates: %d\n", len(updates))
			fmt.Printf("Deletions: %d\n", len(deletedFiles))
			playSoundAsync(upToDateSound, 0.0)
		} else {
			// No updates - minimal output: just status and current version
			fmt.Println("Update available: No")
			if localErr == nil {
				fmt.Printf("Version: %s\n", localVer.String())
			}
		}
	} else {
		// Human-readable output for interactive mode
		if hasUpdates {
			fmt.Printf("\nAn update is available with %d total changes.\n", totalChanges)
			if len(updates) > 0 {
				fmt.Printf("  • %d files will be updated\n", len(updates))
			}
			if len(deletedFiles) > 0 {
				fmt.Printf("  • %d files will be deleted\n", len(deletedFiles))
			}
			if localErr == nil && err == nil {
				fmt.Printf("\nCurrent version: %s\n", localVer.String())
				fmt.Printf("New version: %s\n", latestVer.String())
			}
			if restartRequired {
				fmt.Println("\nNote: This update requires MUSHclient to be restarted.")
			}
			fmt.Println("\nRun the updater again without 'check' to install the update.")
		} else {
			if !quietFlag {
				playSoundAsync(upToDateSound, 0.0)
			}
			fmt.Println("\nAlready up to date!")
			if localErr == nil {
				fmt.Printf("Current version: %s\n", localVer.String())
			}
		}
	}
}

func performUpdates(updates []manifest.FileInfo) error {
	// We already checked if MUSHclient was running earlier in main()

	// If it's a fresh install or lots of files changed, download as one big zip file for speed.
	// Otherwise, download files individually in parallel.
	useZip := !isInstalled() || len(updates) > zipThreshold

	if useZip {
		return downloadZipAndExtract(updates)
	}

	// Download files in parallel (up to fileWorkers at a time)
	sem := make(chan struct{}, fileWorkers)
	var wg sync.WaitGroup
	var updateMutex sync.Mutex
	var downloadErrors []error
	var completedCount int
	total := len(updates)

	if nonInteractive {
		fmt.Println("Downloading...")
	} else if !quietFlag {
		fmt.Printf("\nDownloading %d files...\n", total)
	}

	for i, u := range updates {
		wg.Add(1)
		sem <- struct{}{}
		go func(info manifest.FileInfo, idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := downloadFile(info); err != nil {
				updateMutex.Lock()
				downloadErrors = append(downloadErrors, err)
				updateMutex.Unlock()
			} else {
				updateMutex.Lock()
				completedCount++
				current := completedCount
				updateMutex.Unlock()

				percentage := (current * 100) / total
				// Update title bar with progress
				console.SetTitle(fmt.Sprintf("%s - Downloading: %d%%", title, percentage))

				if nonInteractive {
					// In non-interactive mode, only print percentage
					fmt.Printf("%d%%\n", percentage)
				} else if !quietFlag {
					if verboseFlag {
						fmt.Printf("[%d/%d] (%d%%) %s\n", current, total, percentage, info.Name)
					} else {
						// Show progress without individual file names - single line update
						fmt.Printf("\rProgress: %d/%d (%d%%)    ", current, total, percentage)
					}
				}
			}
		}(u, i)
	}
	wg.Wait()

	if !quietFlag && !verboseFlag && !nonInteractive {
		fmt.Printf("\n") // New line after progress
	}

	if len(downloadErrors) > 0 {
		return fmt.Errorf("failed to update %d files: %v", len(downloadErrors), downloadErrors[0])
	}

	if !quietFlag && !nonInteractive {
		fmt.Println("Saving manifest...")
	}
	// Reset title
	console.SetTitle(title)
	return saveManifest()
}

// grabClient is a shared grab client with retry and timeout settings
var grabClient = grab.NewClient()

func downloadFile(info manifest.FileInfo) error {
	// Never overwrite user configuration files
	if paths.IsUserConfig(info.Name) {
		if verboseFlag {
			log.Printf("Skipping user config file: %s\n", info.Name)
		}
		return nil
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Normalize the file path from manifest (forward slashes) to platform format
	relativePath := paths.Denormalize(info.Name)
	targetPath := filepath.Join(baseDir, relativePath)

	// Ensure target path doesn't escape the base directory
	absTargetPath, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("failed to resolve path for %s: %w", info.Name, err)
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("failed to resolve base directory: %w", err)
	}
	if !strings.HasPrefix(absTargetPath, absBaseDir) {
		return fmt.Errorf("path traversal attempt detected: %s", info.Name)
	}

	// Find actual path in case of case mismatch
	targetPath, err = paths.FindActual(absTargetPath)
	if err != nil {
		return fmt.Errorf("failed to find path for %s: %w", info.Name, err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", info.Name, err)
	}

	// Create grab request
	req, err := grab.NewRequest(targetPath, info.URL)
	if err != nil {
		return fmt.Errorf("failed to create request for %s: %w", info.Name, err)
	}
	req.NoResume = true // Always overwrite, never resume

	// Download with retry
	resp := grabClient.Do(req)

	// Wait for completion
	if err := resp.Err(); err != nil {
		return fmt.Errorf("failed to download %s: %w", info.Name, err)
	}

	return nil
}

func downloadAndExtractZip(zipURL string, targetDir string, isInstall bool, filesToExtract []manifest.FileInfo) error {
	if nonInteractive {
		fmt.Println("Downloading...")
	} else if !quietFlag {
		fmt.Printf("Downloading archive...\n")
	}
	// Play downloading sound during fresh installation download
	if isInstall {
		playSoundAsyncLoop(downloadingSound, 0.0, true) // Normal volume for downloading sound, looping
	}

	// Create temp file for download
	tempFile, err := os.CreateTemp("", "miriani-update-*.zip")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	tempFile.Close()
	defer os.Remove(tempPath) // Clean up temp file when done

	// Create grab request for ZIP download
	req, err := grab.NewRequest(tempPath, zipURL)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}
	req.NoResume = true // Always overwrite, never resume

	// Start download
	resp := grabClient.Do(req)

	// Progress reporting loop
	lastPercentage := -1
	lastMB := int64(-1)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

progressLoop:
	for {
		select {
		case <-ticker.C:
			// Check if we have content length for percentage progress
			if resp.Size() > 0 {
				percentage := int(resp.Progress() * 100)
				if percentage != lastPercentage {
					console.SetTitle(fmt.Sprintf("%s - Downloading: %d%%", title, percentage))
					if nonInteractive {
						fmt.Printf("%d%%\n", percentage)
					} else if !quietFlag && !verboseFlag {
						fmt.Printf("\rDownloading: %d%%    ", percentage)
					}
					lastPercentage = percentage
				}
			} else {
				// No content length - show MB downloaded instead
				mb := resp.BytesComplete() / (1024 * 1024)
				if mb != lastMB {
					if !quietFlag && !verboseFlag && !nonInteractive {
						fmt.Printf("\rDownloading: %d MB    ", mb)
					}
					lastMB = mb
				}
			}
		case <-resp.Done:
			break progressLoop
		}
	}

	if !quietFlag && !verboseFlag && !nonInteractive {
		fmt.Printf("\n")
	}

	// Check for download errors
	if err := resp.Err(); err != nil {
		return fmt.Errorf("failed to download archive: %w", err)
	}

	if nonInteractive {
		fmt.Println("Extracting...")
	} else if !quietFlag {
		fmt.Printf("Extracting files...\n")
	}

	// Stop any currently playing sounds (like download music) before starting extraction
	stopAllSounds()

	// Play installing sound during extraction (for fresh installs)
	if isInstall {
		playSoundAsyncLoop(installingSound, -1.5, true) // Slightly lower volume for installing sound, looping
	}

	// Open downloaded ZIP file
	zipFile, err := os.Open(tempPath)
	if err != nil {
		return fmt.Errorf("failed to open downloaded archive: %w", err)
	}
	defer zipFile.Close()

	zipStat, err := zipFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat downloaded archive: %w", err)
	}

	r, err := zip.NewReader(zipFile, zipStat.Size())
	if err != nil {
		return fmt.Errorf("failed to parse archive: %w", err)
	}

	absTargetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("failed to resolve target directory: %w", err)
	}

	// GitHub ZIP archives include a top-level directory named "repo-branch"
	// We need to strip this prefix when extracting
	var stripPrefix string
	if len(r.File) > 0 {
		// Detect the strip prefix from the first file
		firstPath := r.File[0].Name
		if idx := strings.Index(firstPath, "/"); idx != -1 {
			stripPrefix = firstPath[:idx+1]
		}
	}

	// Build a map of files to extract for quick lookup (if filtering is enabled)
	var extractFilter map[string]bool
	if len(filesToExtract) > 0 {
		extractFilter = make(map[string]bool, len(filesToExtract))
		for _, f := range filesToExtract {
			normalizedPath := paths.Normalize(f.Name)
			extractFilter[normalizedPath] = true
		}
	}

	totalFiles := len(r.File)
	extractedFiles := 0
	skippedFiles := 0
	lastReportedPercentage := -1

	for _, f := range r.File {
		// Strip the GitHub repo-branch prefix
		relPath := f.Name
		if stripPrefix != "" && strings.HasPrefix(relPath, stripPrefix) {
			relPath = strings.TrimPrefix(relPath, stripPrefix)
		}

		// Skip if nothing left after stripping
		if relPath == "" {
			continue
		}

		// If we have a file filter (for updates), skip files not in the filter
		if extractFilter != nil {
			normalizedRelPath := paths.Normalize(relPath)
			if !extractFilter[normalizedRelPath] {
				skippedFiles++
				if verboseFlag && !nonInteractive {
					fmt.Printf("Skipping (not needed): %s\n", relPath)
				}
				continue
			}
		}

		// Skip user configuration files during updates (but not during fresh install)
		if !isInstall && paths.IsUserConfig(relPath) {
			// Check if file already exists - only skip if it exists
			filePath := filepath.Join(absTargetDir, paths.Denormalize(relPath))
			if _, err := os.Stat(filePath); err == nil {
				if !quietFlag && verboseFlag && !nonInteractive {
					fmt.Printf("Preserving existing user config file: %s\n", relPath)
				}
				continue
			}
			// File doesn't exist, install it even though it's a config file
		}

		// Archive paths use forward slashes, normalize to platform format
		normalizedPath := paths.Denormalize(relPath)
		fpath := filepath.Join(absTargetDir, normalizedPath)

		// Security: Ensure path doesn't escape base directory
		absFpath, err := filepath.Abs(fpath)
		if err != nil {
			return fmt.Errorf("failed to resolve path for %s: %w", relPath, err)
		}
		if !strings.HasPrefix(absFpath, absTargetDir) {
			return fmt.Errorf("path traversal attempt detected in archive: %s", relPath)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(absFpath, f.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", absFpath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(absFpath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", absFpath, err)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open file in archive %s: %w", relPath, err)
		}

		out, err := os.OpenFile(absFpath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return fmt.Errorf("failed to create file %s: %w", absFpath, err)
		}

		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return fmt.Errorf("failed to write file %s: %w", absFpath, err)
		}

		extractedFiles++
		percentage := (extractedFiles * 100) / totalFiles
		// Update title bar with progress
		console.SetTitle(fmt.Sprintf("%s - Extracting: %d%%", title, percentage))

		if nonInteractive {
			// Only print at meaningful intervals to avoid spam
			// Scale interval based on number of files: more files = finer granularity
			var interval int
			if totalFiles < 100 {
				interval = 25 // 25%, 50%, 75%, 100%
			} else if totalFiles < 1000 {
				interval = 10 // 10%, 20%, 30%...
			} else {
				interval = 5 // 5%, 10%, 15%...
			}

			if percentage != lastReportedPercentage && (percentage%interval == 0 || percentage == 100) {
				fmt.Printf("%d%%\n", percentage)
				lastReportedPercentage = percentage
			}
		} else if !quietFlag {
			if verboseFlag {
				fmt.Printf("[%d/%d] (%d%%) %s\n", extractedFiles, totalFiles, percentage, relPath)
			} else {
				// Single line progress update
				fmt.Printf("\rProgress: %d/%d (%d%%)    ", extractedFiles, totalFiles, percentage)
			}
		}
	}

	if !quietFlag && !nonInteractive {
		if !verboseFlag {
			fmt.Printf("\n") // New line after progress
		}
		if extractFilter != nil {
			fmt.Printf("Extraction complete! (%d files extracted, %d skipped)\n", extractedFiles, skippedFiles)
		} else {
			fmt.Println("Extraction complete!")
		}
	}

	// Reset title
	console.SetTitle(title)
	return nil
}

func downloadZipAndExtract(updates []manifest.FileInfo) error {
	zipURL, err := getZipURLForChannel()
	if err != nil {
		return err
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	if err := downloadAndExtractZip(zipURL, baseDir, false, updates); err != nil {
		return err
	}

	if !quietFlag && !nonInteractive {
		fmt.Println("Saving manifest...")
	}
	return saveManifest()
}

// ============================================================================
// SECTION 4: MANIFEST MANAGEMENT
// ============================================================================

func loadRemoteManifest() (map[string]manifest.FileInfo, error) {
	var ref string

	if channelFlag == "stable" {
		// For stable, get latest tag
		tag, err := getLatestTag()
		if err != nil {
			return nil, fmt.Errorf("failed to get latest tag: %w", err)
		}
		ref = tag
		if !quietFlag && verboseFlag {
			fmt.Printf("Using stable tag: %s\n", tag)
		}
	} else if channelFlag == "dev" {
		// For dev, use main branch (latest commit)
		ref = "main"
		if !quietFlag && verboseFlag {
			fmt.Printf("Using dev: main branch (latest commit)\n")
		}
	} else {
		// For custom branches, use the branch name directly
		ref = channelFlag
		if !quietFlag && verboseFlag {
			fmt.Printf("Using experimental branch: %s\n", ref)
		}
	}

	// Get tree from GitHub API
	tree, err := getGitHubTree(ref)
	if err != nil {
		return nil, fmt.Errorf("failed to get file tree: %w", err)
	}

	// Convert tree to manifest format
	fileManifest := make(map[string]manifest.FileInfo)
	for _, item := range tree.Tree {
		// Only include files (blobs), not directories (trees)
		if item.Type != "blob" {
			continue
		}

		// Skip excluded files
		if manifestManager.ShouldExclude(item.Path, paths.Normalize) {
			continue
		}

		// Normalize path
		normalizedPath := paths.Normalize(item.Path)

		// Generate raw URL
		rawURL := getRawURLForTag(ref, item.Path)

		fileManifest[normalizedPath] = manifest.FileInfo{
			Name: normalizedPath,
			Hash: item.SHA, // Git SHA-1 hash from GitHub API
			URL:  rawURL,
		}
	}

	if !quietFlag && verboseFlag {
		fmt.Printf("Found %d files in repository\n", len(fileManifest))
	}

	return fileManifest, nil
}

func saveManifest() error {
	// Get remote manifest (from GitHub API)
	remoteManifest, err := loadRemoteManifest()
	if err != nil {
		return fmt.Errorf("failed to load remote manifest: %w", err)
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Only save files to local manifest that exist both in remote AND locally on disk
	// This ensures the local manifest accurately represents what's actually installed
	localManifest := make(map[string]manifest.FileInfo)
	for path, info := range remoteManifest {
		filePath := filepath.Join(baseDir, paths.Denormalize(path))
		if _, err := os.Stat(filePath); err == nil {
			// File exists locally, include it in the local manifest
			localManifest[path] = info
		}
	}

	// Save to local file
	data, err := json.MarshalIndent(localManifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	if err := os.WriteFile(filepath.Join(baseDir, manifestFile), append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("failed to save manifest: %w", err)
	}

	return nil
}

// ============================================================================
// SECTION 6: INSTALLATION
// ============================================================================

func handleInstallation() (string, error) {
	// Determine default installation directory
	usr, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	defaultInstallDir := filepath.Join(usr, "Documents", "Miriani-Next")

	fmt.Println("Welcome to the Miriani-Next installer.")

	// Check for embedded data early
	hasEmbedded := embedded.HasData()
	embeddedVersion := ""
	if hasEmbedded {
		embeddedVersion = embedded.GetVersion()
		fmt.Printf("\nInstalling v%s\n", embeddedVersion)
	}

	// If no channel was explicitly set, prompt for selection during fresh install
	if !channelExplicitlySet && !nonInteractive {
		channelFlag = promptForChannelWithOptions(hasEmbedded)
	}

	// Determine installation directory
	installDir := defaultInstallDir

	// Ask if user wants to change the default location
	if !nonInteractive {
		fmt.Printf("\nDefault installation location: %s\n", defaultInstallDir)
		if confirmAction("Do you want to change the installation location?") {
			selectedDir, err := promptForInstallFolder(defaultInstallDir)
			if err != nil {
				fmt.Printf("Error selecting folder: %v\n", err)
				fmt.Printf("Using default location: %s\n", defaultInstallDir)
			} else {
				installDir = selectedDir
			}
		}
	}

	// Determine shortcut name
	shortcutName := "Miriani-Next"
	if shortcutNameFlag != "" {
		shortcutName = shortcutNameFlag
	}

	if !nonInteractive {
		fmt.Printf("\nEnter name for the desktop shortcut [default: %s]: ", shortcutName)
		reader := bufio.NewReader(os.Stdin)
		if response, err := reader.ReadString('\n'); err == nil {
			response = strings.TrimSpace(response)
			if response != "" {
				shortcutName = response
			}
		}
	}

	fmt.Printf("\nThis will install the %s version to: %s\n", channelFlag, installDir)

	// Check if MUSHclient is running before installation
	if process.IsMUSHClientRunningInDir(installDir) {
		if nonInteractive {
			// In non-interactive mode, kill MUSHclient before installing
			console.Log("MUSHclient is running. Killing MUSHclient before installation...")
			if err := process.KillMUSHClient(installDir); err != nil {
				console.Log("Error: failed to kill MUSHclient: %v", err)
				return "", fmt.Errorf("failed to kill MUSHclient: %w", err)
			}
			console.Log("MUSHclient killed successfully. Proceeding with installation...")
			// Wait for process to fully terminate
			if !process.WaitForMUSHClientTermination(installDir, 5*time.Second) {
				console.Log("Warning: MUSHclient may not have fully terminated")
			}
		} else {
			// In interactive mode, tell user to close it
			fmt.Println("\nMUSHclient is running and needs to be closed to update it.")
			fmt.Println("Please close MUSHclient before proceeding with installation.")
			playSound(errorSound)
			waitForUser("\nPress Enter to exit...")
			return "", fmt.Errorf("MUSHclient is running")
		}
	}

	if !confirmAction("Do you want to proceed with the installation?") {
		fmt.Println("Installation cancelled.")
		return "", ErrUserCancelled
	}

	// Create installation directory
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create installation directory: %w", err)
	}

	if !quietFlag {
		fmt.Printf("\nInstalling to: %s\n", installDir)
	}

	// Use embedded data if available (offline installer)
	if hasEmbedded {
		return installFromEmbedded(installDir, embeddedVersion)
	}

	// Get the appropriate zipball
	zipURL, err := getZipURLForChannel()
	if err != nil {
		return "", err
	}

	if !quietFlag && verboseFlag {
		if channelFlag == "stable" {
			tag, _ := getLatestTag()
			fmt.Printf("Installing from tag: %s\n", tag)
		} else if channelFlag == "dev" {
			fmt.Println("Installing from main branch (latest commit)")
		} else {
			fmt.Printf("Installing from experimental branch: %s\n", channelFlag)
		}
	}

	// Download and extract the archive (isInstall = true, no file filter = extract all)
	if err := downloadAndExtractZip(zipURL, installDir, true, nil); err != nil {
		return "", fmt.Errorf("failed to download installation: %w", err)
	}

	// Change to installation directory for manifest save
	originalDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}
	if err := os.Chdir(installDir); err != nil {
		return "", fmt.Errorf("failed to change to installation directory: %w", err)
	}

	// Save a local manifest for future updates
	if !quietFlag {
		fmt.Println("Saving manifest...")
	}
	if err := saveManifest(); err != nil {
		// Non-fatal - just warn
		fmt.Printf("Warning: failed to save manifest: %v\n", err)
	}

	// Save channel preference
	if err := saveChannel(channelFlag); err != nil {
		// Non-fatal - just warn
		fmt.Printf("Warning: failed to save channel preference: %v\n", err)
	} else if !quietFlag && verboseFlag {
		fmt.Printf("Saved channel preference: %s\n", channelFlag)
	}

	// Save version.json with the installed version
	if latestVer, err := getLatestVersion(); err == nil {
		if versionData, err := json.MarshalIndent(latestVer, "", "  "); err == nil {
			if err := os.WriteFile(versionFile, versionData, 0644); err != nil {
				fmt.Printf("Warning: failed to save version file: %v\n", err)
			} else if !quietFlag && verboseFlag {
				fmt.Printf("Saved version: %s\n", latestVer.String())
			}
		}
	}

	// Create .updater-excludes file to protect user configuration
	if err := createUpdaterExcludes(); err != nil {
		// Non-fatal - just warn
		fmt.Printf("Warning: failed to create .updater-excludes: %v\n", err)
	} else if !quietFlag && verboseFlag {
		fmt.Println("Created .updater-excludes file")
	}

	// Create channel switching batch files
	if err := install.CreateChannelSwitchBatchFiles(installDir); err != nil {
		// Non-fatal - just warn
		fmt.Printf("Warning: failed to create channel switch batch files: %v\n", err)
	} else if !quietFlag && verboseFlag {
		fmt.Println("Created channel switching batch files (switch-to-stable.bat, switch-to-dev.bat)")
	}

	if !quietFlag {
		fmt.Println("\nInstallation complete!")
		fmt.Println("Location:", installDir)
	}

	// Check for MUDMixer or Proxiani and offer to configure world file
	// Prioritize MUDMixer if both are running
	proxianiDetected := isProxianiRunning()
	mudmixerDetected := isMUDMixerRunning()

	if (proxianiDetected || mudmixerDetected) && !nonInteractive {
		if mudmixerDetected {
			// Play sound first, then wait before showing messages
			go playSoundWithDucking(proxianiSound, 0.3)
			time.Sleep(300 * time.Millisecond)

			fmt.Println("\nMUDMixer detected!")
			fmt.Println("MUDMixer is a local proxy server that can provide additional features.")
			fmt.Println("Would you like to configure Miriani-Next to connect through MUDMixer?")
			fmt.Println("(This changes the connection from " + defaultServer + " to " + localServer + ":" + mudMixerPort + ")")

			if confirmAction("Configure Miriani to use MUDMixer?") {
				worldFilePath := filepath.Join(installDir, worldsDir, worldFileName)
				if err := updateWorldFileForMUDMixer(worldFilePath); err != nil {
					fmt.Printf("Warning: failed to update world file for MUDMixer: %v\n", err)
				} else {
					fmt.Println("World file updated successfully!")
					fmt.Println("Miriani-Next will now connect through MUDMixer (" + localServer + ":" + mudMixerPort + ")")
				}
			} else {
				fmt.Println("Skipping MUDMixer configuration. You can manually change this later.")
			}
		} else if proxianiDetected {
			// Play sound first, then wait before showing messages
			go playSoundWithDucking(proxianiSound, 0.3)
			time.Sleep(300 * time.Millisecond)

			fmt.Println("\nProxiani detected!")
			fmt.Println("Proxiani is a local proxy server that can provide additional features.")
			fmt.Println("Would you like to configure Miriani-Next to connect through Proxiani?")
			fmt.Println("(This changes the connection from " + defaultServer + " to " + localServer + ":" + proxianiPort + ")")

			if confirmAction("Configure Miriani to use Proxiani?") {
				worldFilePath := filepath.Join(installDir, worldsDir, worldFileName)
				if err := updateWorldFileForProxiani(worldFilePath); err != nil {
					fmt.Printf("Warning: failed to update world file for Proxiani: %v\n", err)
				} else {
					fmt.Println("World file updated successfully!")
					fmt.Println("Miriani-Next will now connect through Proxiani (" + localServer + ":" + proxianiPort + ")")
				}
			} else {
				fmt.Println("Skipping Proxiani configuration. You can manually change this later.")
			}
		}
	} else if (proxianiDetected || mudmixerDetected) && nonInteractive {
		// In non-interactive mode, auto-configure (prioritize MUDMixer)
		if mudmixerDetected {
			console.Log("MUDMixer detected! Auto-configuring world file...")
			worldFilePath := filepath.Join(installDir, worldsDir, worldFileName)
			if err := updateWorldFileForMUDMixer(worldFilePath); err != nil {
				console.Log("Warning: failed to update world file for MUDMixer: %v", err)
			} else {
				console.Log("World file updated successfully for MUDMixer")
			}
		} else if proxianiDetected {
			console.Log("Proxiani detected! Auto-configuring world file...")
			worldFilePath := filepath.Join(installDir, worldsDir, worldFileName)
			if err := updateWorldFileForProxiani(worldFilePath); err != nil {
				console.Log("Warning: failed to update world file for Proxiani: %v", err)
			} else {
				console.Log("World file updated successfully for Proxiani")
			}
		}
	}

	// Create desktop icon (wrapped in panic recovery to prevent COM crashes)
	func() {
		defer func() {
			if r := recover(); r != nil {
				if !quietFlag {
					fmt.Printf("Warning: failed to create desktop icon: %v\n", r)
				}
			}
		}()
		if err := createDesktopIcon(installDir, shortcutName); err != nil {
			if !quietFlag {
				fmt.Printf("Warning: failed to create desktop icon: %v\n", err)
			}
		} else if !quietFlag {
			fmt.Println("Desktop shortcut created!")
		}
	}()

	// Move updater to installation directory (AFTER everything is done)
	exePath, err := os.Executable()
	if err == nil {
		// Always name the destination file "update.exe"
		destPath := filepath.Join(installDir, "update.exe")
		// Only move if not already in install dir with correct name
		absExePath, _ := filepath.Abs(exePath)
		absDestPath, _ := filepath.Abs(destPath)

		if absExePath != absDestPath {
			// Go back to original directory before moving
			os.Chdir(originalDir)

			// Copy first, then remove original only if copy succeeded
			data, err := os.ReadFile(exePath)
			if err == nil {
				if err := os.WriteFile(destPath, data, 0755); err == nil {
					// Successfully copied, now safe to remove original
					os.Remove(exePath)
				}
			}
		}
	}

	return installDir, nil
}

// ------------------------
// UPDATER MANAGEMENT
// ------------------------
func copyUpdaterToInstallation(installDir string) error {
	// Get current executable path
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Read current executable
	data, err := os.ReadFile(exePath)
	if err != nil {
		return fmt.Errorf("failed to read updater: %w", err)
	}

	// Write to installation directory as update.exe
	destPath := filepath.Join(installDir, "update.exe")
	if err := os.WriteFile(destPath, data, 0755); err != nil {
		return fmt.Errorf("failed to write updater: %w", err)
	}

	return nil
}

// ============================================================================
// SECTION 7: PROCESS DETECTION (delegated to internal/process)
// ============================================================================

func isProxianiRunning() bool {
	return process.IsNodeListeningOnPort(proxianiPort)
}

// ============================================================================
// SECTION 8: WORLD FILE UPDATES
// ============================================================================

var worldFileConfig = install.WorldFileConfig{
	DefaultServer: defaultServer,
	LocalServer:   localServer,
	ProxianiPort:  proxianiPort,
	MUDMixerPort:  mudMixerPort,
}

func updateWorldFile(worldFilePath string, updatePort bool) error {
	return install.UpdateWorldFile(worldFilePath, updatePort, worldFileConfig)
}

func updateWorldFileForProxiani(worldFilePath string) error {
	return updateWorldFile(worldFilePath, false)
}

func isMUDMixerRunning() bool {
	return process.IsPortListening(mudMixerPort)
}

func updateWorldFileForMUDMixer(worldFilePath string) error {
	return updateWorldFile(worldFilePath, true)
}

// ============================================================================
// SECTION 11: INSTALLATION DETECTION
// ============================================================================

func isInstalled() bool {
	baseDir, err := os.Getwd()
	if err != nil {
		return false
	}
	return install.IsInstalled(baseDir)
}

func hasWorldFilesInCurrentDir() bool {
	baseDir, err := os.Getwd()
	if err != nil {
		return false
	}
	return install.HasWorldFiles(baseDir)
}

// ============================================================================
// SECTION 16: MISCELLANEOUS
// ============================================================================

func needsMUSHClientRestart(updates []manifest.FileInfo) bool {
	for _, file := range updates {
		lowerName := strings.ToLower(file.Name)
		if lowerName == "mushclient.exe" || strings.HasSuffix(lowerName, ".dll") {
			return true
		}
	}
	return false
}

func isMUSHClientRunning() bool {
	baseDir, err := os.Getwd()
	if err != nil {
		return false
	}
	return process.IsMUSHClientRunningInDir(baseDir)
}

func launchMUSHClient() error {
	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Check if MUSHclient is already running to prevent duplicate instances
	if process.IsMUSHClientRunningInDir(baseDir) {
		return fmt.Errorf("MUSHclient is already running")
	}

	exePath := filepath.Join(baseDir, "MUSHclient.exe")
	if _, err := os.Stat(exePath); err != nil {
		return fmt.Errorf("MUSHclient.exe not found: %w", err)
	}

	if err := exec.Command(exePath).Start(); err != nil {
		return fmt.Errorf("failed to launch MUSHclient: %w", err)
	}

	return nil
}

func loadExcludes() map[string]struct{} {
	baseDir, err := os.Getwd()
	if err != nil {
		return make(map[string]struct{})
	}
	return paths.LoadExcludes(filepath.Join(baseDir, excludesFile))
}

// ------------------------
// UTILITIES
// ------------------------

// fatalError shows an error, plays a sound, and waits for user to acknowledge in interactive mode
func fatalError(format string, args ...interface{}) {
	// Play error sound to notify user
	playSoundAsync(errorSound, 0.0)

	// Display the error message
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	} else {
		fmt.Fprintln(os.Stderr, format)
	}

	// In interactive mode, wait for user to press Enter
	if !nonInteractive {
		waitForUser("\nPress Enter to exit...")
	}

	os.Exit(1)
}

// moveToOldFolder moves a file to the .old directory instead of deleting it
func moveToOldFolder(filePath string, relativePath string) error {
	baseDir, err := os.Getwd()
	if err != nil {
		return err
	}

	// Create .old directory if it doesn't exist
	oldDir := filepath.Join(baseDir, ".old")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		return err
	}

	// Create subdirectories in .old if needed
	oldFilePath := filepath.Join(oldDir, paths.Denormalize(relativePath))
	if err := os.MkdirAll(filepath.Dir(oldFilePath), 0755); err != nil {
		return err
	}

	// Move the file
	return os.Rename(filePath, oldFilePath)
}

func cleanOldFolder() error {
	baseDir, err := os.Getwd()
	if err != nil {
		return err
	}

	oldDir := filepath.Join(baseDir, ".old")
	if _, err := os.Stat(oldDir); err == nil {
		if !quietFlag && verboseFlag {
			fmt.Println("Cleaning up .old directory from previous run...")
		}
		return os.RemoveAll(oldDir)
	}
	return nil
}

func createUpdaterExcludes() error {
	baseDir, err := os.Getwd()
	if err != nil {
		return err
	}

	var content strings.Builder
	content.WriteString("# Updater Exclusions\n")
	content.WriteString("# This file lists paths that the updater will NEVER touch.\n")
	content.WriteString("# These are typically user configuration files and data.\n")
	content.WriteString("#\n")
	content.WriteString("# Lines starting with # are comments.\n")
	content.WriteString("# One path per line.\n")
	content.WriteString("# Paths are relative to the installation directory.\n")
	content.WriteString("#\n")
	content.WriteString("# DO NOT delete this file unless you want the updater to\n")
	content.WriteString("# potentially overwrite your configuration!\n")
	content.WriteString("\n")
	content.WriteString("# MUSHclient configuration files\n")
	content.WriteString("mushclient.ini\n")
	content.WriteString("mushclient_prefs.sqlite\n")
	content.WriteString("\n")
	content.WriteString("# World configuration files (*.mcl files in worlds directory)\n")
	content.WriteString("worlds/*.mcl\n")
	content.WriteString("\n")

	excludesPath := filepath.Join(baseDir, excludesFile)
	return os.WriteFile(excludesPath, []byte(content.String()), 0644)
}

// ============================================================================
// SECTION 14: CHANGELOG/RELEASE NOTES
// ============================================================================

func buildChangelog(updates []manifest.FileInfo, deletedFiles []string) string {
	return changelog.Build(updates, deletedFiles, changelog.BuildConfig{
		Channel: channelFlag,
	})
}

// showChangelog displays updated and deleted files and offers to open in notepad
func showChangelog(updates []manifest.FileInfo, deletedFiles []string) {
	totalChanges := len(updates) + len(deletedFiles)
	fmt.Printf("\n%d files were changed (%d updated, %d deleted)\n", totalChanges, len(updates), len(deletedFiles))

	// Build the changelog content
	changelogContent := buildChangelog(updates, deletedFiles)

	// Ask if user wants to view changelog
	if !nonInteractive && confirmAction("Would you like to view the detailed changelog?") {
		// Write to temp file
		tmpFile := filepath.Join(os.TempDir(), "next-changelog.txt")
		if err := os.WriteFile(tmpFile, []byte(changelogContent), 0644); err == nil {
			// Open with notepad
			exec.Command("notepad.exe", tmpFile).Start()
		}
	}
}

func waitForUser(p string) {
	prompt.WaitForKey(p, promptConfig())
}

// ============================================================================
// SECTION 13: PROMPTING/MENUS (delegated to internal/prompt)
// ============================================================================

func promptForInstallFolder(defaultPath string) (string, error) {
	return prompt.SelectFolder(defaultPath, promptConfig())
}

func confirmAction(p string) bool {
	return prompt.Confirm(p, promptConfig())
}

// ============================================================================
// SECTION 10: CHANNEL MANAGEMENT
// ============================================================================

func saveChannel(ch string) error {
	baseDir, err := os.Getwd()
	if err != nil {
		return err
	}
	return channel.Save(baseDir, ch)
}

func loadChannel() (string, error) {
	baseDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return channel.Load(baseDir)
}

func isValidChannel(channel string) bool {
	// Always allow stable and dev
	if channel == "stable" || channel == "dev" {
		return true
	}

	// Check if it's a valid branch name
	branches, err := ghClient.GetBranches()
	if err != nil {
		// If we can't fetch branches, only allow stable/dev
		return false
	}

	for _, branch := range branches {
		if branch.Name == channel {
			return true
		}
	}

	return false
}

func promptInstallationMenu(existingInstallFound bool, detectedPath string, toastushPath string) string {
	return prompt.InstallationMenu(existingInstallFound, detectedPath, toastushPath, promptConfig())
}

func promptForChannel() string {
	return promptForChannelWithOptions(false)
}

func promptForChannelWithOptions(forFutureUpdates bool) string {
	info := prompt.ChannelInfo{
		ForFutureUpdates: forFutureUpdates,
	}
	if tag, err := getLatestTag(); err == nil {
		if date, err := getLastCommitDate(tag); err == nil {
			info.StableDate = date
		}
	}
	if date, err := getLastCommitDate("main"); err == nil {
		info.DevDate = date
	}
	return prompt.ChannelMenu(info, ghClient.GetBranches, promptConfig())
}

// ============================================================================
// SECTION 9: VERSION MANAGEMENT
// ============================================================================

func parseVersionFromTag(tag string) (major, minor, patch int, err error) {
	return version.ParseTag(tag)
}

func getLatestVersion() (*Version, error) {
	var ver Version

	if channelFlag == "stable" {
		// For stable, get latest tag and parse version from it
		tag, err := getLatestTag()
		if err != nil {
			return nil, fmt.Errorf("failed to get latest tag: %w", err)
		}

		major, minor, patch, err := parseVersionFromTag(tag)
		if err != nil {
			return nil, err
		}

		ver.Major = major
		ver.Minor = minor
		ver.Patch = patch
		ver.Commit = "" // Stable releases don't have commit SHA in version
	} else {
		// For dev/experimental, get version from latest tag but include commit SHA
		// First, try to get the latest tag to extract version numbers
		tag, err := getLatestTag()
		if err != nil {
			// If we can't get the tag, fall back to 0.0.0
			ver.Major = 0
			ver.Minor = 0
			ver.Patch = 0
		} else {
			// Try to parse version from tag, fall back to 0.0.0 on error
			major, minor, patch, err := parseVersionFromTag(tag)
			if err == nil {
				ver.Major = major
				ver.Minor = minor
				ver.Patch = patch
			} else {
				// Fall back to 0.0.0 if parsing fails
				ver.Major = 0
				ver.Minor = 0
				ver.Patch = 0
			}
		}

		// Get the commit SHA for the branch
		ref := channelFlag
		if channelFlag == "dev" {
			ref = "main"
		}

		tree, err := getGitHubTree(ref)
		if err != nil {
			return nil, fmt.Errorf("failed to get commit SHA: %w", err)
		}

		// Store first 16 characters of commit SHA
		if len(tree.SHA) >= 16 {
			ver.Commit = tree.SHA[:16]
		} else {
			ver.Commit = tree.SHA
		}

		if !quietFlag && verboseFlag {
			fmt.Printf("Dev channel version: %d.%d.%d+%s\n", ver.Major, ver.Minor, ver.Patch, ver.Commit)
		}
	}

	return &ver, nil
}

func getLocalVersion() (*Version, error) {
	baseDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}
	return version.LoadLocal(baseDir, versionFile)
}

// detectToastushInstallation attempts to find an existing Toastush installation
func detectToastushInstallation() string {
	// Check Documents folder
	usr, err := os.UserHomeDir()
	if err == nil {
		toastushDir := filepath.Join(usr, "Documents", "Toastush")
		if _, err := os.Stat(filepath.Join(toastushDir, "MUSHclient.exe")); err == nil {
			return toastushDir
		}
	}

	// Check desktop shortcuts
	if path := checkDesktopShortcut("Toastush"); path != "" {
		return path
	}

	return ""
}

func getDesktopPath() (string, error) {
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		return "", fmt.Errorf("failed to get user profile directory")
	}

	desktops := []string{
		filepath.Join(userProfile, "Desktop"),
		filepath.Join(userProfile, "OneDrive", "Desktop"),
	}

	for _, desktop := range desktops {
		if _, err := os.Stat(desktop); err == nil {
			return desktop, nil
		}
	}

	return "", fmt.Errorf("desktop directory not found")
}

// checkDesktopShortcut checks for a desktop shortcut and returns its target path
func checkDesktopShortcut(name string) string {
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		return ""
	}

	desktops := []string{
		filepath.Join(userProfile, "Desktop"),
		filepath.Join(userProfile, "OneDrive", "Desktop"),
	}

	for _, desktop := range desktops {
		linkPath := filepath.Join(desktop, name+".lnk")
		if _, err := os.Stat(linkPath); err == nil {
			// Try to read the shortcut target
			if target := getShortcutTarget(linkPath); target != "" {
				// Get the directory containing the target
				targetDir := filepath.Dir(target)
				// Verify it has MUSHclient.exe
				if _, err := os.Stat(filepath.Join(targetDir, "MUSHclient.exe")); err == nil {
					return targetDir
				}
			}
		}
	}

	return ""
}

// getShortcutTarget reads the target path from a Windows shortcut (.lnk file)
func getShortcutTarget(linkPath string) string {
	if err := ole.CoInitialize(0); err != nil {
		return ""
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return ""
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return ""
	}
	defer shell.Release()

	link, err := oleutil.CallMethod(shell, "CreateShortcut", linkPath)
	if err != nil {
		return ""
	}

	linkDisp := link.ToIDispatch()
	defer linkDisp.Release()

	target, err := oleutil.GetProperty(linkDisp, "TargetPath")
	if err != nil {
		return ""
	}

	return target.ToString()
}

// ============================================================================
// SECTION 15: MIGRATION
// ============================================================================

func handleToastushMigration(toastushDir string) error {
	// If we didn't auto-detect an installation, prompt for the directory
	if toastushDir == "" {
		if !nonInteractive {
			fmt.Println("\nLocate your Toastush installation directory")
			selectedDir, err := promptForInstallFolder(filepath.Join(os.Getenv("USERPROFILE"), "Documents"))
			if err != nil {
				return fmt.Errorf("error selecting folder: %w", err)
			}
			toastushDir = selectedDir

			// Verify it's a valid installation
			if _, err := os.Stat(filepath.Join(toastushDir, "MUSHclient.exe")); os.IsNotExist(err) {
				return fmt.Errorf("MUSHclient.exe not found in: %s", toastushDir)
			}
		} else {
			return fmt.Errorf("no Toastush installation found and cannot prompt in non-interactive mode")
		}
	} else {
		// Auto-detected installation - confirm with user
		if !nonInteractive {
			fmt.Printf("\nFound Toastush installation at: %s\n", toastushDir)
			if !confirmAction("Migrate this installation?") {
				fmt.Println("\nLocate your Toastush installation directory")
				selectedDir, err := promptForInstallFolder(filepath.Join(os.Getenv("USERPROFILE"), "Documents"))
				if err != nil {
					return fmt.Errorf("error selecting folder: %w", err)
				}
				toastushDir = selectedDir

				// Verify it's a valid installation
				if _, err := os.Stat(filepath.Join(toastushDir, "MUSHclient.exe")); os.IsNotExist(err) {
					return fmt.Errorf("MUSHclient.exe not found in: %s", toastushDir)
				}
			}
		}
	}

	fmt.Printf("\nMigrating Toastush installation from: %s\n", toastushDir)

	// Check if MUSHclient is running before we do anything
	if process.IsMUSHClientRunningInDir(toastushDir) {
		if nonInteractive {
			console.Log("MUSHclient is running. Killing MUSHclient before migration...")
			if err := process.KillMUSHClient(toastushDir); err != nil {
				return fmt.Errorf("failed to kill MUSHclient: %w", err)
			}
			console.Log("MUSHclient killed successfully")
			// Wait for process to fully terminate
			if !process.WaitForMUSHClientTermination(toastushDir, 5*time.Second) {
				console.Log("Warning: MUSHclient may not have fully terminated")
			}
		} else {
			fmt.Println("\nMUSHclient is currently running and will be closed to proceed with migration.")
			if confirmAction("Kill MUSHclient and continue?") {
				fmt.Println("Closing MUSHclient...")
				if err := process.KillMUSHClient(toastushDir); err != nil {
					fmt.Printf("Error closing MUSHclient: %v\n", err)
					fmt.Println("Please close MUSHclient manually before proceeding.")
					playSound(errorSound)
					waitForUser("\nPress Enter to exit...")
					return fmt.Errorf("failed to close MUSHclient: %w", err)
				}
				fmt.Println("MUSHclient closed successfully.")
				// Wait for process to fully terminate
				if !process.WaitForMUSHClientTermination(toastushDir, 5*time.Second) {
					fmt.Println("Warning: MUSHclient may not have fully terminated")
				}
			} else {
				fmt.Println("Migration cancelled. Please close MUSHclient and run the migration again.")
				return ErrUserCancelled
			}
		}
	}

	// If no channel was explicitly set, prompt for selection
	if !channelExplicitlySet && !nonInteractive {
		channelFlag = promptForChannel()
	}

	// Check if miriani.mcl has been modified from default
	worldFile := filepath.Join(toastushDir, worldsDir, worldFileName)
	mclModified := false
	if hash, err := hashFile(worldFile); err == nil {
		if hash != defaultToastushMCLHash {
			mclModified = true
		}
	}

	// Warn about miriani.mcl if it's been modified
	if mclModified && !nonInteractive {
		fmt.Println("\nWARNING: Modifications detected in miriani.mcl")
		fmt.Println("The installer will replace this file.")
		fmt.Println("This may result in loss of custom connection details or world names/configurations.")
		fmt.Println()
		fmt.Println("NOTE: Miriani-Next has an entirely different configuration system.")
		fmt.Println("Settings in toastush:config will NOT be migrated.")
		fmt.Println()
		if !confirmAction("Continue with migration?") {
			return ErrUserCancelled
		}
	}

	if !quietFlag {
		fmt.Printf("\nInstalling Miriani-Next files to: %s\n", toastushDir)
	}

	// Get the appropriate zipball
	zipURL, err := getZipURLForChannel()
	if err != nil {
		return err
	}

	// Download and extract (as fresh install to replace all files, no file filter = extract all)
	if err := downloadAndExtractZip(zipURL, toastushDir, true, nil); err != nil {
		return fmt.Errorf("failed to download Miriani-Next files: %w", err)
	}

	// Rename directory to Miriani-Next
	newDir := filepath.Join(filepath.Dir(toastushDir), "Miriani-Next")
	if toastushDir != newDir {
		// Check if target already exists
		if _, err := os.Stat(newDir); err == nil {
			if !nonInteractive {
				fmt.Printf("\nDirectory already exists: %s\n", newDir)
				if !confirmAction("Remove existing Miriani-Next directory and continue?") {
					return fmt.Errorf("migration cancelled by user")
				}
			}
			// Remove existing directory
			if err := os.RemoveAll(newDir); err != nil {
				return fmt.Errorf("failed to remove existing directory: %w", err)
			}
		}

		if !quietFlag {
			fmt.Printf("\nRenaming directory to: %s\n", newDir)
		}
		if err := os.Rename(toastushDir, newDir); err != nil {
			return fmt.Errorf("failed to rename directory: %w", err)
		}
		toastushDir = newDir
	}

	// Change to installation directory
	if err := os.Chdir(toastushDir); err != nil {
		return fmt.Errorf("failed to change to installation directory: %w", err)
	}

	// Generate manifest
	if err := saveManifest(); err != nil {
		fmt.Printf("Warning: failed to generate manifest: %v\n", err)
	}

	// Save channel preference
	if err := saveChannel(channelFlag); err != nil {
		fmt.Printf("Warning: failed to save channel preference: %v\n", err)
	}

	// Save version.json with the installed version
	if latestVer, err := getLatestVersion(); err == nil {
		if versionData, err := json.MarshalIndent(latestVer, "", "  "); err == nil {
			if err := os.WriteFile(versionFile, versionData, 0644); err != nil {
				fmt.Printf("Warning: failed to save version file: %v\n", err)
			} else if !quietFlag && verboseFlag {
				fmt.Printf("Saved version: %s\n", latestVer.String())
			}
		}
	}

	// Create channel switching batch files
	if err := install.CreateChannelSwitchBatchFiles(toastushDir); err != nil {
		fmt.Printf("Warning: failed to create channel switch batch files: %v\n", err)
	}

	// Copy updater to installation
	if err := copyUpdaterToInstallation(toastushDir); err != nil {
		fmt.Printf("Warning: failed to copy updater: %v\n", err)
	}

	// Update desktop shortcut
	if !quietFlag {
		fmt.Println("\nUpdating desktop shortcut...")
	}
	if err := createDesktopIcon(toastushDir, "Miriani-Next"); err != nil {
		if !quietFlag {
			fmt.Printf("Warning: failed to update desktop shortcut: %v\n", err)
		}
	} else if !quietFlag {
		fmt.Println("Desktop shortcut updated!")
	}

	if !quietFlag {
		fmt.Println("\nMigration complete!")
		fmt.Println("Location:", toastushDir)
	}

	return nil
}

// hashFile calculates the SHA1 hash of a file
func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha1.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func createDesktopIcon(targetDir string, name string) error {
	if err := ole.CoInitialize(0); err != nil {
		return fmt.Errorf("failed to initialize COM: %w", err)
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("failed to create WScript.Shell: %w", err)
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("failed to query shell interface: %w", err)
	}
	defer shell.Release()

	// Get desktop path
	desktop, err := getDesktopPath()
	if err != nil {
		return err
	}

	if !strings.HasSuffix(strings.ToLower(name), ".lnk") {
		name = name + ".lnk"
	}
	linkPath := filepath.Join(desktop, name)

	link, err := oleutil.CallMethod(shell, "CreateShortcut", linkPath)
	if err != nil {
		return fmt.Errorf("failed to create shortcut: %w", err)
	}
	// Don't call link.Clear() - it causes crashes

	linkDisp := link.ToIDispatch()
	defer linkDisp.Release()

	if _, err := oleutil.PutProperty(linkDisp, "TargetPath", filepath.Join(targetDir, "MUSHclient.exe")); err != nil {
		return fmt.Errorf("failed to set shortcut target: %w", err)
	}
	if _, err := oleutil.PutProperty(linkDisp, "WorkingDirectory", targetDir); err != nil {
		return fmt.Errorf("failed to set shortcut working directory: %w", err)
	}
	if _, err := oleutil.PutProperty(linkDisp, "Description", "Launch Miriani-Next"); err != nil {
		return fmt.Errorf("failed to set shortcut description: %w", err)
	}
	if _, err := oleutil.PutProperty(linkDisp, "WindowStyle", 1); err != nil {
		return fmt.Errorf("failed to set shortcut window style: %w", err)
	}
	_, err = oleutil.CallMethod(linkDisp, "Save")
	if err != nil {
		return fmt.Errorf("failed to save shortcut: %w", err)
	}

	return nil
}

// installFromEmbedded installs using embedded release data (offline installer).
// The embedded ZIP should contain .manifest and version.json from the release.
func installFromEmbedded(installDir string, embeddedVersion string) (string, error) {
	// Play installation sound asynchronously so it doesn't block extraction
	playSoundAsyncLoop(installingSound, -1.5, true)

	if !quietFlag {
		fmt.Println("Extracting files...")
	}

	// Extract embedded files (includes .manifest and version.json if present)
	total := 0
	err := embedded.ExtractTo(installDir, func(cur, tot int, filename string) {
		total = tot
		percentage := (cur * 100) / tot

		// Update title bar with progress
		console.SetTitle(fmt.Sprintf("%s - Extracting: %d%%", title, percentage))

		if !quietFlag {
			if verboseFlag {
				fmt.Printf("[%d/%d] (%d%%) %s\n", cur, tot, percentage, filename)
			} else {
				fmt.Printf("\rProgress: %d/%d (%d%%)    ", cur, tot, percentage)
			}
		}
	})
	if err != nil {
		return "", fmt.Errorf("failed to extract embedded files: %w", err)
	}

	if !quietFlag && !verboseFlag {
		fmt.Printf("\n") // New line after progress
	}
	fmt.Printf("Extracted %d files.\n", total)

	// Change to installation directory
	originalDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}
	if err := os.Chdir(installDir); err != nil {
		return "", fmt.Errorf("failed to change to installation directory: %w", err)
	}
	defer os.Chdir(originalDir)

	// Check if manifest was extracted, if not generate one
	if _, err := os.Stat(manifestFile); os.IsNotExist(err) {
		if !quietFlag {
			fmt.Println("Generating manifest...")
		}
		if err := saveManifest(); err != nil {
			fmt.Printf("Warning: failed to generate manifest: %v\n", err)
		}
	} else if !quietFlag && verboseFlag {
		fmt.Println("Using embedded manifest")
	}

	// Check if version.json was extracted, if not create one
	if _, err := os.Stat(versionFile); os.IsNotExist(err) {
		ver := &version.Version{}
		if major, minor, patch, err := version.ParseTag("v" + embeddedVersion); err == nil {
			ver.Major = major
			ver.Minor = minor
			ver.Patch = patch
		}
		if data, err := json.MarshalIndent(ver, "", "  "); err == nil {
			os.WriteFile(versionFile, data, 0644)
		}
	}

	// Save channel preference (default to stable for offline installs)
	if channelFlag == "" {
		channelFlag = "stable"
	}
	if err := saveChannel(channelFlag); err != nil {
		fmt.Printf("Warning: failed to save channel preference: %v\n", err)
	}

	// Create .updater-excludes file if it doesn't exist
	if _, err := os.Stat(excludesFile); os.IsNotExist(err) {
		if err := createUpdaterExcludes(); err != nil {
			fmt.Printf("Warning: failed to create .updater-excludes: %v\n", err)
		}
	}

	// Create channel switching batch files
	if err := install.CreateChannelSwitchBatchFiles(installDir); err != nil {
		fmt.Printf("Warning: failed to create channel switch batch files: %v\n", err)
	}

	// Download slim updater to replace the fat offline installer
	if err := downloadSlimUpdater(installDir); err != nil {
		fmt.Printf("Warning: failed to download updater: %v\n", err)
		fmt.Println("You can manually download it from: https://github.com/spacemangaming/next-launcher/releases")
	}

	if !quietFlag {
		fmt.Println("\nInstallation complete!")
		fmt.Println("Location:", installDir)
		fmt.Printf("Version: %s (offline installer)\n", embeddedVersion)
	}

	playSound(successSound)

	return installDir, nil
}

// downloadSlimUpdater downloads miriani.exe from GitHub releases and saves as update.exe.
// This replaces the fat offline installer with the slim updater for future updates.
func downloadSlimUpdater(installDir string) error {
	// GitHub releases URL for latest updater
	updaterURL := "https://github.com/spacemangaming/next-launcher/releases/latest/download/miriani.exe"
	targetPath := filepath.Join(installDir, "update.exe")

	// Download to temp file first
	resp, err := http.Get(updaterURL)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Create temp file in same directory (for atomic rename)
	tmpFile, err := os.CreateTemp(installDir, "updater-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up on error

	// Copy with progress
	totalSize := resp.ContentLength
	written := int64(0)
	buf := make([]byte, 32*1024)
	lastPercent := -1

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := tmpFile.Write(buf[:n])
			if writeErr != nil {
				tmpFile.Close()
				return fmt.Errorf("failed to write file: %w", writeErr)
			}
			written += int64(n)

			// Show progress
			if totalSize > 0 && !quietFlag {
				percent := int(written * 100 / totalSize)
				if percent != lastPercent {
					console.SetTitle(fmt.Sprintf("%s - Downloading updater: %d%%", title, percent))
					fmt.Printf("\rDownloading updater: %d%%    ", percent)
					lastPercent = percent
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmpFile.Close()
			return fmt.Errorf("failed to read response: %w", readErr)
		}
	}
	tmpFile.Close()

	if !quietFlag {
		fmt.Printf("\n")
	}

	// Rename to final location
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("failed to rename: %w", err)
	}

	return nil
}
