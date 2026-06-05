# Miriani-Next Updater

![Tests](https://github.com/spacemangaming/next-launcher/actions/workflows/test.yml/badge.svg)

A Windows-based auto-updater and launcher for the Miriani-Next scripts. This updater provides robust update management, installation handling, and seamless channel switching between stable releases and development builds.

## Features

- **Automatic Updates** - Manifest-based file synchronization with GitHub
- **Multiple Update Channels** - Switch between stable, dev, or any custom branch
- **Smart Downloads** - Differential updates (only changed files) with intelligent fallback to ZIP archives
- **Audio Feedback** - Optional sound effects for download, installation, and error states
- **Self-Updating** - The updater can update itself
- **Migration Support** - Seamless migration from legacy Toastush installations
- **Process Management** - Detects and manages MUSHclient instances during updates, giving the option for restarts
- **Non-Interactive Mode** - Silent operation for automated workflows

## System Requirements

- **Operating System**: Windows 10 or later
- **Runtime**: No external dependencies (standalone executable)
- **Storage**: ~50-100 MB for full installation
- **Network**: Internet connection for updates, if not using an offline version

## Installation

### Fresh Installation

1. Download `miriani.exe` from the latest release
2. Run the file from any location
3. Follow the interactive prompts to:
   - Choose installation directory (default: `%USERPROFILE%\Documents\Miriani-Next`)
   - Select update channel (stable or dev)
   - Configure server preferences (Proxiani or MUDMixer)

The updater will automatically:
- Download all necessary files
- Create desktop shortcuts
- Generate channel switching batch files
- Configure world files for the selected server

### Updating Existing Installation

Simply run `update.exe` from your installation directory. The updater will:
- Check for updates on your current channel
- Download and apply changes
- Display a changelog of updates
- Optionally restart MUSHclient after updating

## Usage

### Basic Commands

```bash
# Update current installation
update.exe

# Check for updates without applying
update check

# Switch update channels
update switch stable
update switch dev
```

### Command-Line Flags

| Flag | Description |
|------|-------------|
| `-channel <name>` | Specify update channel (stable, dev, or branch name) |
| `-quiet` | Suppress all output except errors |
| `-verbose` | Show detailed operation information |
| `-non-interactive` | Run without user prompts (writes result to `.update-result`) |
| `-allow-restart` | Allow automatic MUSHclient restart after update |
| `-generate-manifest` | Generate manifest file for current directory |
| `-version` | Display updater version |

### Examples

```bash
# Silent update with detailed logging
update -quiet -verbose -non-interactive

# Switch to dev channel and update
update -channel dev

# Check for updates on a specific branch
update check -channel feature/new-ui
```

## Update Channels

The updater supports three types of channels:

- **stable** - Latest tagged release (recommended for most users)
- **dev** - Latest commit on the main branch (cutting edge features)
- **custom** - Any GitHub branch name (for testing specific features)

### Switching Channels

You can switch channels using:
1. Command line: `update switch <channel>`
2. Batch files (generated in installation directory):
   - `Switch to Stable.bat`
   - `Switch to Dev.bat`
   - `Switch to Any Channel.bat`

**Note**: Switching from dev/custom to stable will check for downgrades and warn if you're attempting to downgrade.

## How It Works

### Manifest-Based Updates

The updater uses a manifest system to track installed files:
- `.manifest` - JSON file mapping paths to SHA-1 hashes and download URLs
- Compares local manifest with GitHub repository tree
- Downloads only changed files (differential updates)
- Automatically switches to ZIP archive download for large updates (30+ files)

### Update Process

1. **Check** - Compare local manifest with GitHub repository
2. **Download** - Fetch changed files (individual or ZIP archive)
3. **Verify** - Validate file integrity using SHA-1 hashes
4. **Apply** - Extract/copy files to installation directory
5. **Cleanup** - Remove deleted files, update manifest

### File Protection

User configuration files are never overwritten:
- `mushclient_prefs.sqlite`
- `mushclient.ini`
- `worlds/*.mcl` (world files)
- `worlds/plugins/state/*` (plugin state)
- `logs/*`
- `worlds/settings/*`

## Configuration Files

### Runtime Files

| File | Purpose |
|------|---------|
| `.manifest` | Tracks installed files with hashes and URLs |
| `.update-channel` | Current update channel name |
| `.updater-excludes` | Custom file exclusion patterns (glob format) |
| `.update-result` | JSON result from non-interactive updates |
| `version.json` | Current installation version metadata |

### Exclusion Patterns

Create `.updater-excludes` to prevent specific files from being updated:

```
# Exclude all log files
logs/*.log

# Exclude specific directories
temp/
cache/

# Exclude file patterns
*.backup
```

## Building from Source

### Prerequisites

- Go 1.24.0 or later
- Windows SDK (for syscall support)

### Build Instructions

```bash
# Clone the repository
git clone https://github.com/spacemangaming/miriani-next.git
cd miriani-next

# Build with version info
build.bat 1.0.0

# Or use Go directly
go build -trimpath -ldflags="-s -w -X main.version=1.0.0" -o update
```

### Build Flags

- `-trimpath` - Remove file system paths from binary
- `-s -w` - Strip debug symbols
- `-X main.version=<version>` - Inject version string

## Architecture

### Project Structure

```
next-launcher/
├── updater.go              # Main application (3,615 lines)
├── internal/
│   ├── audio/              # Audio playback system
│   ├── channel/            # Update channel persistence
│   ├── console/            # Windows console management
│   ├── download/           # File download utilities
│   ├── github/             # GitHub API client
│   ├── install/            # Installation utilities
│   ├── manifest/           # Manifest CRUD operations
│   ├── paths/              # Path normalization and validation
│   └── process/            # Process detection (MUSHclient, servers)
├── sounds/                 # Embedded WAV audio files
└── build.bat               # Build script
```

### Key Components

- **GitHub Integration** - Fetches release info, commits, and file trees from `spacemangaming/miriani-next`
- **Audio System** - WAV playback with volume control, ducking, and async support (uses beep library)
- **Console Management** - Windows console attachment, title setting, user prompts
- **Download Manager** - Concurrent downloads (6 workers), progress tracking, path validation
- **Manifest Manager** - JSON with comment support, exclusion patterns, file filtering

### Dependencies

- `github.com/cavaliergopher/grab/v3` - HTTP download with progress
- `github.com/gopxl/beep` - Audio playback
- `github.com/go-ole/go-ole` - Windows COM/OLE (desktop shortcuts)

## Security

### Path Traversal Protection

All file operations validate paths to prevent directory traversal attacks:
- Download paths are validated before writing
- ZIP extraction checks each entry path
- User cannot specify paths outside installation directory

### Update Integrity

- SHA-1 hash verification for all downloaded files
- TLS for all GitHub API and download connections
- Manifest stored locally to detect tampering

## Troubleshooting

### Common Issues

**"MUSHclient is currently running"**
- Close MUSHclient before updating
- Or use `-allow-restart` to automatically restart after update

**"Failed to download file"**
- Check internet connection
- Verify firewall isn't blocking update
- Check GitHub API rate limits (60 requests/hour for unauthenticated)

**"Manifest file is corrupted"**
- Delete `.manifest` file
- Run updater again to regenerate manifest

**Updates not appearing**
- Verify you're on the correct channel: `update check -verbose`
- Check if channel has newer commits: visit GitHub repository

### Debug Mode

Run with `-verbose` flag for detailed operation logs:

```bash
update -verbose
```

### Non-Interactive Results

When running with `-non-interactive`, results are written to `.update-result`:

```json
{
  "success": true,
  "message": "Update completed successfully",
  "filesUpdated": 15,
  "version": "1.2.3"
}
```

### Testing

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run with race detection
go test -race ./...
```

---

**Note**: This updater is specifically designed for the Miriani-Next MUSHclient application and is not a general-purpose updater. (Yet!)
