# Uninstall Grove from GitHub Releases install.
#
#   irm https://raw.githubusercontent.com/provasign/grove/main/uninstall.ps1 | iex
#
# Parameters (pass as env vars or dot-source):
#   $env:INSTALL_DIR   directory where grove was installed   (default: $HOME\bin)
#
[CmdletBinding()]
param(
  [string]$InstallDir = $env:INSTALL_DIR
)
$ErrorActionPreference = "Stop"
$PRODUCT = "grove"
if (-not $InstallDir) { $InstallDir = "$env:USERPROFILE\bin" }

function ok($msg) { Write-Host "✅ $msg" -ForegroundColor Green }
function info($msg) { Write-Host "==> $msg" -ForegroundColor Blue }

$target = "$InstallDir\$PRODUCT.exe"
if (Test-Path $target) {
  Remove-Item $target -Force
  ok "removed $target"
} else {
  info "$target : not found (already removed?)"
}

Write-Host ""
Write-Host "$PRODUCT uninstalled from $InstallDir"
Write-Host "PATH note: if this was your only Provasign tool, remove $InstallDir from your user PATH manually."
