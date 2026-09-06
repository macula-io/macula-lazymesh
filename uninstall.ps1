# Uninstalls lazymesh: removes the binary install.ps1 placed. Leaves
# $env:USERPROFILE\.config\lazymesh alone by default -- it holds the
# persisted mesh identity (real puzzle-grinding work to generate), the
# isolated contact_policy.json (past "Answer + Trust" ring decisions),
# config.yaml, and the local-tools workspace, if enabled. Pass -Purge to
# remove all of that too.
#
# Usage:
#   irm https://raw.githubusercontent.com/macula-io/macula-lazymesh/main/uninstall.ps1 | iex
#   # or, to pass -Purge, download first:
#   iwr -useb https://raw.githubusercontent.com/macula-io/macula-lazymesh/main/uninstall.ps1 -OutFile uninstall.ps1
#   .\uninstall.ps1 -Purge
#
# Env overrides:
#   $env:LAZYMESH_INSTALL_DIR  where to look for the binary (default: $env:LOCALAPPDATA\lazymesh)

param(
    [switch]$Purge
)

$ErrorActionPreference = "Stop"

$InstallDir = if ($env:LAZYMESH_INSTALL_DIR) { $env:LAZYMESH_INSTALL_DIR } else { "$env:LOCALAPPDATA\lazymesh" }
$BinPath = Join-Path $InstallDir "lazymesh.exe"
# lazymesh always resolves its config/identity directory via Go's
# os.UserHomeDir() plus a literal ".config\lazymesh" join -- unlike
# macula-cli it does NOT use os.UserConfigDir()/%AppData%, so on Windows
# this is $env:USERPROFILE\.config\lazymesh, not a subfolder of
# $InstallDir and not %AppData% either (see internal/config/config.go's
# Default()/DefaultPath()).
$ConfigDir = Join-Path "$env:USERPROFILE\.config" "lazymesh"

if (Test-Path $BinPath) {
    Remove-Item -Force $BinPath
    Write-Host "removed $BinPath"
} else {
    Write-Host "no binary found at $BinPath (already removed, or installed elsewhere -- set `$env:LAZYMESH_INSTALL_DIR)"
}

if ($Purge) {
    if (Test-Path $ConfigDir) {
        Remove-Item -Recurse -Force $ConfigDir
        Write-Host "removed $ConfigDir (-Purge: identity, contact policy, config, and workspace all deleted too)"
    } else {
        Write-Host "no config directory found at $ConfigDir"
    }
} elseif (Test-Path $ConfigDir) {
    Write-Host "left $ConfigDir in place (identity, contact policy, config, workspace) -- pass -Purge to remove it too"
}
