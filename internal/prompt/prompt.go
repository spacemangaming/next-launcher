package prompt

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"

	"github.com/spacemangaming/next-launcher/internal/github"
)

// SoundPlayer defines the interface for playing sounds
type SoundPlayer interface {
	Play(name string)
	PlayAsync(name string)
}

// Config holds configuration for prompting
type Config struct {
	NonInteractive  bool
	Sound           SoundPlayer
	GetConsoleWindow func() uintptr
}

// WaitForKey waits for user to press Enter
func WaitForKey(prompt string, cfg Config) {
	if cfg.NonInteractive {
		return
	}
	fmt.Print(prompt)
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

// Confirm asks the user to confirm an action
func Confirm(prompt string, cfg Config) bool {
	if cfg.NonInteractive {
		return true
	}

	fmt.Printf("%s (y/n): ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	confirmed := response == "y" || response == "yes"

	if cfg.Sound != nil {
		if confirmed || response == "n" || response == "no" {
			cfg.Sound.Play("select")
		}
		if confirmed {
			cfg.Sound.Play("success")
		}
	}

	return confirmed
}

// SelectFolder opens a folder selection dialog
func SelectFolder(defaultPath string, cfg Config) (string, error) {
	if cfg.NonInteractive {
		return defaultPath, nil
	}

	fmt.Println("\nPress Enter to select installation folder...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')

	consoleHandle := uintptr(0)
	if cfg.GetConsoleWindow != nil {
		consoleHandle = cfg.GetConsoleWindow()
	}

	ole.CoInitialize(0)
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("Shell.Application")
	if err != nil {
		return "", fmt.Errorf("failed to create Shell object: %w", err)
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return "", fmt.Errorf("failed to get IDispatch interface: %w", err)
	}
	defer shell.Release()

	folderObj, err := oleutil.CallMethod(shell, "BrowseForFolder", int(consoleHandle),
		"Select installation folder for Miriani-Aura", 0x10)
	if err != nil {
		return "", fmt.Errorf("failed to show folder dialog: %w", err)
	}

	if folderObj.Value() == nil {
		return "", fmt.Errorf("folder selection cancelled")
	}

	folderItem := folderObj.ToIDispatch()
	if folderItem == nil {
		return "", fmt.Errorf("folder selection cancelled")
	}
	defer folderItem.Release()

	selfProp, err := oleutil.GetProperty(folderItem, "Self")
	if err != nil {
		return "", fmt.Errorf("failed to get folder item: %w", err)
	}

	selfDispatch := selfProp.ToIDispatch()
	defer selfDispatch.Release()

	pathProp, err := oleutil.GetProperty(selfDispatch, "Path")
	if err != nil {
		return "", fmt.Errorf("failed to get folder path: %w", err)
	}

	selectedPath := pathProp.ToString()
	if selectedPath == "" {
		return "", fmt.Errorf("no folder selected")
	}

	return selectedPath, nil
}

// InstallationMenu displays an interactive menu for installation options
// Returns "1" for install, "2" for add updater, "3" for migrate, "" for cancel
func InstallationMenu(existingInstallFound bool, detectedPath string, toastushPath string, cfg Config) string {
	fmt.Println("\nMiriani-Aura Installation")
	fmt.Println()

	if existingInstallFound {
		fmt.Printf("Detected existing installation at: %s\n", detectedPath)
		fmt.Println()
	}

	if toastushPath != "" {
		fmt.Printf("Detected Toastush installation at: %s\n", toastushPath)
		fmt.Println()
	}

	fmt.Println("  1. Install")
	fmt.Println("     Full installation of Miriani-Aura")
	fmt.Println()
	fmt.Println("  2. Install Updater")
	fmt.Println("     Add the updater to an existing Miriani-Aura installation")
	fmt.Println()
	fmt.Println("  3. Migrate from Toastush")
	fmt.Println("     Upgrade existing Toastush installation to Miriani-Aura")
	fmt.Println()
	fmt.Print("Enter your choice (1, 2, or 3): ")

	reader := bufio.NewReader(os.Stdin)
	for {
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("\nError reading input, cancelling installation.")
			return ""
		}

		response = strings.TrimSpace(response)
		switch response {
		case "1", "2", "3":
			if cfg.Sound != nil {
				cfg.Sound.PlayAsync("select")
			}
			return response
		default:
			fmt.Print("Invalid choice. Please enter 1, 2, or 3: ")
		}
	}
}

// ChannelInfo provides info about a channel for display
type ChannelInfo struct {
	StableDate       string
	DevDate          string
	ForFutureUpdates bool // When true, indicates channel selection is for future updates only
}

// ChannelMenu displays an interactive menu to select update channel
// Returns "stable", "dev", or a branch name
func ChannelMenu(info ChannelInfo, getBranches func() ([]github.Branch, error), cfg Config) string {
	if info.ForFutureUpdates {
		fmt.Println("\nSelect Update Channel for future updates")
	} else {
		fmt.Println("\nMiriani-Aura Update Channel Selection")
	}
	fmt.Println()
	fmt.Println("Update channels control how often you receive updates:")
	fmt.Println()

	stableDate := ""
	if info.StableDate != "" {
		stableDate = fmt.Sprintf(" (Last updated: %s)", info.StableDate)
	}

	devDate := ""
	if info.DevDate != "" {
		devDate = fmt.Sprintf(" (Last updated: %s)", info.DevDate)
	}

	fmt.Printf("  1. Stable%s\n", stableDate)
	fmt.Println("     Tested, stable releases only")
	fmt.Println("     Updates less frequently but very reliable")
	fmt.Println("     Recommended for most users")
	fmt.Println()
	fmt.Printf("  2. Dev%s\n", devDate)
	fmt.Println("     Latest features and bug fixes")
	fmt.Println("     Updates frequently with new changes")
	fmt.Println("     May occasionally have bugs")
	fmt.Println()
	fmt.Println("  3. Other")
	fmt.Println("     Follow a specific experimental branch")
	fmt.Println("     For advanced users and testing only")
	fmt.Println()
	fmt.Print("Enter your choice (1, 2, or 3): ")

	reader := bufio.NewReader(os.Stdin)
	for {
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("\nError reading input, defaulting to stable.")
			return "stable"
		}

		response = strings.TrimSpace(response)
		switch response {
		case "1":
			if cfg.Sound != nil {
				cfg.Sound.Play("select")
				cfg.Sound.PlayAsync("success")
			}
			fmt.Println("\nUsing the stable channel.")
			return "stable"
		case "2":
			if cfg.Sound != nil {
				cfg.Sound.Play("select")
				cfg.Sound.PlayAsync("success")
			}
			fmt.Println("\nUsing the dev channel.")
			return "dev"
		case "3":
			if cfg.Sound != nil {
				cfg.Sound.Play("select")
				cfg.Sound.PlayAsync("success")
			}
			return BranchMenu(getBranches, cfg)
		default:
			fmt.Print("Invalid choice. Please enter 1, 2, or 3: ")
		}
	}
}

// BranchMenu displays a menu to select an experimental branch
func BranchMenu(getBranches func() ([]github.Branch, error), cfg Config) string {
	fmt.Println("\nExperimental Branch Selection")
	fmt.Println()
	fmt.Println("Fetching available branches...")

	branches, err := getBranches()
	if err != nil {
		fmt.Printf("Error fetching branches: %v\n", err)
		return ChannelMenu(ChannelInfo{}, getBranches, cfg)
	}

	// Filter out main (that's "dev")
	var experimentalBranches []github.Branch
	for _, branch := range branches {
		if branch.Name != "main" {
			experimentalBranches = append(experimentalBranches, branch)
		}
	}

	if len(experimentalBranches) == 0 {
		fmt.Println("No experimental branches available.")
		return ChannelMenu(ChannelInfo{}, getBranches, cfg)
	}

	fmt.Println("\nAvailable experimental branches:")
	fmt.Println()
	for i, branch := range experimentalBranches {
		fmt.Printf("  %d. %s (commit: %s)\n", i+1, branch.Name, branch.Commit.SHA[:7])
	}
	fmt.Println()
	fmt.Printf("Enter choice (1-%d) or 0 to go back: ", len(experimentalBranches))

	reader := bufio.NewReader(os.Stdin)
	for {
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("\nError reading input, returning to main menu.")
			return ChannelMenu(ChannelInfo{}, getBranches, cfg)
		}

		response = strings.TrimSpace(response)
		if response == "0" {
			return ChannelMenu(ChannelInfo{}, getBranches, cfg)
		}

		choice := 0
		fmt.Sscanf(response, "%d", &choice)
		if choice >= 1 && choice <= len(experimentalBranches) {
			selectedBranch := experimentalBranches[choice-1].Name
			if cfg.Sound != nil {
				cfg.Sound.Play("select")
				cfg.Sound.PlayAsync("success")
			}
			fmt.Printf("\nSelected branch: %s\n", selectedBranch)
			fmt.Println("\nWARNING: Experimental branches may be unstable!")
			fmt.Println("Only use this if you know what you're doing.")
			return selectedBranch
		} else {
			fmt.Printf("Invalid choice. Please enter 0-%d: ", len(experimentalBranches))
		}
	}
}
