#requires -RunAsAdministrator
<#
.SYNOPSIS
  Re-applies the custom AC3/EAC3 (+HEVC) Electron runtime to Seanime Denshi.

.DESCRIPTION
  Denshi uses electron-updater. Every auto-update overwrites our custom-built
  Electron with the stock one, which has no Dolby AC-3/E-AC-3 decoders, so AC3/
  EAC3 audio goes silent again (GitHub issue #508). Run this after any update.

  It rebuilds the install from $DistZip (the patched Electron v39.2.7 runtime,
  built per chromium.md) overlaid with Denshi's CURRENT app files, so it always
  keeps whatever app.asar the update shipped.

  Old install is renamed to "<install>.backup" (not deleted) for rollback.

  ponytail: assumes the update kept Electron v39.2.7. If Denshi bumps Electron to
  a new major, this dist.zip may mismatch the new app.asar -> rebuild from source
  (chromium.md) to refresh H:\electron-gn\dist.zip, then re-run this.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\reapply-electron-ac3.ps1
#>
param(
    [string]$Install = "C:\Program Files\Seanime Denshi",
    [string]$DistZip = "H:\electron-gn\dist.zip"
)
$ErrorActionPreference = "Stop"

if (-not (Test-Path $DistZip)) { throw "Patched runtime not found: $DistZip (rebuild per chromium.md)" }
if (-not (Test-Path $Install)) { throw "Denshi install not found: $Install" }

$stage  = Join-Path $env:TEMP "denshi-reapply-stage"
$backup = "$Install.backup"

Write-Host "[1/5] Stopping Denshi..."
Get-Process | Where-Object { $_.ProcessName -match "Seanime Denshi|seanime-server" } |
    Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep 2

Write-Host "[2/5] Staging patched runtime from $DistZip ..."
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
Expand-Archive $DistZip $stage -Force

Write-Host "[3/5] Overlaying Denshi's current app files..."
Remove-Item "$stage\resources\default_app.asar" -Force -ErrorAction SilentlyContinue
Copy-Item "$Install\resources\app.asar"       "$stage\resources\" -Force
Copy-Item "$Install\resources\app-update.yml" "$stage\resources\" -Force -ErrorAction SilentlyContinue
Copy-Item "$Install\resources\elevate.exe"    "$stage\resources\" -Force -ErrorAction SilentlyContinue
Copy-Item "$Install\resources\binaries"       "$stage\resources\binaries" -Recurse -Force -ErrorAction SilentlyContinue
Rename-Item "$stage\electron.exe" "Seanime Denshi.exe"
Copy-Item "$Install\Uninstall Seanime Denshi.exe" "$stage\" -Force -ErrorAction SilentlyContinue

Write-Host "[4/5] Backing up current install -> $backup"
if (Test-Path $backup) { Remove-Item $backup -Recurse -Force }
Rename-Item $Install $backup

Write-Host "[5/5] Installing patched runtime..."
Copy-Item $stage $Install -Recurse -Force
Remove-Item $stage -Recurse -Force

Write-Host "Done. AC3/EAC3 runtime re-applied. Launch Denshi and play an AC3/EAC3 file."
Write-Host "Rollback if needed: remove '$Install' and rename '$backup' back."
