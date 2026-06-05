@echo off
setlocal

set VERSION=%~1
if "%VERSION%"=="" (
    set VERSION=dev
)

echo Generating resources...
go generate
if %errorlevel% neq 0 (
    echo [WARNING] Failed to generate resources, building anyway...
)

echo Building updater v%VERSION%...
go build -trimpath -ldflags="-s -w -X main.appVersion=%VERSION%" -o update.exe
if %errorlevel% equ 0 (
    echo [OK] Successfully built update.exe!
) else (
    echo [ERROR] Build failed with code %errorlevel%
)

endlocal
