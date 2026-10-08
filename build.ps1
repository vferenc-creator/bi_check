# Builds dist\BIMonitor.exe on Windows.
#   .\build.ps1              version from git tag, or 0.0.0-dev
#   .\build.ps1 -Version 1.2.0
param([string]$Version = "")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
if (-not $Version) {
  $Version = (git describe --tags --abbrev=0 2>$null)
  if (-not $Version) { $Version = "0.0.0-dev" }
}
$Version = $Version.TrimStart("v")
Write-Host "BI Output Monitor $Version"
go run ./tools/genres -version $Version
if ($LASTEXITCODE) { exit $LASTEXITCODE }
New-Item -ItemType Directory -Force dist | Out-Null
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-H windowsgui -s -w -X bimonitor/internal/app.Version=$Version" -o dist/BIMonitor.exe ./cmd/bimonitor
if ($LASTEXITCODE) { exit $LASTEXITCODE }
Get-Item dist/BIMonitor.exe | Format-List Name, Length, LastWriteTime
