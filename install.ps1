# lazylocal Windows Installer
# Run via PowerShell: irm https://raw.githubusercontent.com/kuarezma/lazylocal/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

$repo = "kuarezma/lazylocal"
$installDir = Join-Path $env:USERPROFILE ".local\bin"

# Detect architecture
$arch = "amd64"
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
    $arch = "arm64"
} elseif (-not [System.Environment]::Is64BitOperatingSystem) {
    Write-Error "lazylocal requires a 64-bit Windows system (x64 or ARM64)."
    exit 1
}

$binary = "lazylocal-windows-$arch.exe"
$url = "https://github.com/$repo/releases/latest/download/$binary"

Write-Host "⚡ Downloading lazylocal (windows/$arch) from $url..." -ForegroundColor Cyan

if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}

$targetExe = Join-Path $installDir "lazylocal.exe"
$tempFile = [System.IO.Path]::GetTempFileName()

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $url -OutFile $tempFile -UseBasicParsing
    Move-Item -Path $tempFile -Destination $targetExe -Force
} catch {
    Write-Error "Failed to download lazylocal: $_"
    if (Test-Path $tempFile) { Remove-Item -Path $tempFile -Force }
    exit 1
}

Write-Host "✅ Successfully installed lazylocal to $targetExe" -ForegroundColor Green

# Check if $installDir is in user PATH
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    Write-Host "`n⚠️  Note: $installDir is not currently in your PATH." -ForegroundColor Yellow
    Write-Host "To add it permanently to your User PATH, run:" -ForegroundColor Yellow
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', `$([Environment]::GetEnvironmentVariable('Path', 'User') + ';$installDir'), 'User')" -ForegroundColor Cyan
    Write-Host "Or in your current PowerShell session:" -ForegroundColor Yellow
    Write-Host "  `$env:Path += ';$installDir'" -ForegroundColor Cyan
}

try {
    & $targetExe --version
} catch {
    # ignore if unable to execute directly in same turn
}
