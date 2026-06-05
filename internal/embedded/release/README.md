# Embedded Release Files

This directory contains files for building the offline installer.

## Building Without Embedded Files (Normal)

```bash
go build
```

This produces a normal updater that downloads files from GitHub.

## Building With Embedded Files (Offline Installer)

1. Download the release ZIP:
   ```bash
   gh release download v4.0.17 --repo spacemangaming/Miriani-Aura --pattern 'Miriani-Aura.zip' --dir internal/embedded/release
   ```

2. Build with the embedded tag:
   ```bash
   go build -tags embedded -o miriani-installer.exe
   ```

The ZIP already contains `.manifest` and `version.json` from the release.
The version is read directly from version.json in the embedded ZIP.

## Automated Build

Use the GitHub Actions workflow to automatically build the offline installer.
See `.github/workflows/build-offline-installer.yml`
