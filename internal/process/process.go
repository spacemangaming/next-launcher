package process

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/distantorigin/next-launcher/internal/paths"
)

// IsNodeListeningOnPort checks if node.exe is running and listening on the specified port
func IsNodeListeningOnPort(port string) bool {
	// Check if node.exe is running
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq node.exe", "/FO", "CSV", "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	// If no node.exe processes, not running
	if !strings.Contains(string(output), "node.exe") {
		return false
	}

	// Check if port is in use
	return IsPortListening(port)
}

// IsPortListening checks if a TCP port is in LISTENING state
func IsPortListening(port string) bool {
	cmd := exec.Command("netstat", "-ano", "-p", "tcp")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, ":"+port) && strings.Contains(line, "LISTENING") {
			return true
		}
	}

	return false
}

// IsMUSHClientRunningInDir checks if MUSHclient.exe is running from the specified directory
func IsMUSHClientRunningInDir(targetDir string) bool {
	expectedPath := paths.CleanLower(filepath.Join(targetDir, "MUSHclient.exe"))

	// Use WMIC to get all running MUSHclient.exe processes with their full paths
	cmd := exec.Command("wmic", "process", "where", "name='MUSHclient.exe'", "get", "ExecutablePath", "/format:list")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	// Parse output - format is "ExecutablePath=C:\path\to\MUSHclient.exe"
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecutablePath=") {
			processPath := paths.CleanLower(strings.TrimPrefix(line, "ExecutablePath="))
			if processPath == expectedPath {
				return true
			}
		}
	}

	return false
}

// KillMUSHClient kills only MUSHclient.exe processes running from the specified directory,
// leaving MUSHclient instances from other directories untouched.
func KillMUSHClient(targetDir string) error {
	pids := getMUSHClientPIDs(targetDir)
	if len(pids) == 0 {
		return fmt.Errorf("no MUSHclient.exe processes found in %s", targetDir)
	}

	var errs []string
	for _, pid := range pids {
		if err := exec.Command("taskkill", "/PID", pid, "/F").Run(); err != nil {
			errs = append(errs, fmt.Sprintf("PID %s: %v", pid, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to kill some processes: %s", strings.Join(errs, "; "))
	}
	return nil
}

// getMUSHClientPIDs returns the PIDs of MUSHclient.exe processes running from the specified directory.
func getMUSHClientPIDs(targetDir string) []string {
	expectedPath := paths.CleanLower(filepath.Join(targetDir, "MUSHclient.exe"))

	cmd := exec.Command("wmic", "process", "where", "name='MUSHclient.exe'", "get", "ExecutablePath,ProcessId", "/format:list")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	// WMIC outputs blocks separated by blank lines, each block has ExecutablePath= and ProcessId= lines
	var pids []string
	var currentPath string
	var currentPID string

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecutablePath=") {
			currentPath = paths.CleanLower(strings.TrimPrefix(line, "ExecutablePath="))
		} else if strings.HasPrefix(line, "ProcessId=") {
			currentPID = strings.TrimPrefix(line, "ProcessId=")
		}

		// When we have both values, check if this is a match
		if currentPath != "" && currentPID != "" {
			if currentPath == expectedPath {
				pids = append(pids, currentPID)
			}
			currentPath = ""
			currentPID = ""
		}
	}

	return pids
}

// WaitForMUSHClientTermination polls until no MUSHclient.exe processes from the
// specified directory are running. Returns true if terminated, false on timeout.
func WaitForMUSHClientTermination(targetDir string, timeout time.Duration) bool {
	start := time.Now()
	for time.Since(start) < timeout {
		if !IsMUSHClientRunningInDir(targetDir) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// WaitForTermination polls until the specified process is no longer running
// Returns true if process terminated, false if timeout occurred
func WaitForTermination(processName string, timeout time.Duration) bool {
	start := time.Now()
	for time.Since(start) < timeout {
		cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("IMAGENAME eq %s", processName), "/NH")
		output, err := cmd.Output()
		if err != nil {
			// If tasklist fails, assume process is not running
			return true
		}

		outputStr := strings.ToLower(string(output))
		if !strings.Contains(outputStr, strings.ToLower(processName)) {
			return true
		}

		time.Sleep(100 * time.Millisecond)
	}
	return false
}
