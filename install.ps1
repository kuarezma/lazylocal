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
$checksumUrl = "https://github.com/$repo/releases/latest/download/checksums.txt"

Write-Host "⚡ Downloading lazylocal (windows/$arch) from $url..." -ForegroundColor Cyan

if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}

$targetExe = Join-Path $installDir "lazylocal.exe"
$tempFile = [System.IO.Path]::GetTempFileName()
$tempChecksums = [System.IO.Path]::GetTempFileName()

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $url -OutFile $tempFile -UseBasicParsing

    Write-Host "🔒 Verifying SHA-256 checksum..." -ForegroundColor Cyan
    try {
        $expectedHash = ""
        if ($env:EXPECTED_SHA256 -and $env:EXPECTED_SHA256.Trim() -ne "") {
            $expectedHash = $env:EXPECTED_SHA256.Trim().ToLower()
        } else {
            Invoke-WebRequest -Uri $checksumUrl -OutFile $tempChecksums -UseBasicParsing
            $lines = Get-Content $tempChecksums
            foreach ($line in $lines) {
                if ($line -match "([a-fA-F0-9]{64})\s+.*$binary") {
                    $expectedHash = $matches[1].ToLower()
                    break
                }
            }
            if (-not $expectedHash) {
                throw "Could not find checksum for $binary in checksums.txt"
            }
        }
        $actualHash = (Get-FileHash -Path $tempFile -Algorithm SHA256).Hash.ToLower()
        if ($expectedHash -ne $actualHash) {
            throw "SHA-256 checksum mismatch! Expected: $expectedHash, Actual: $actualHash"
        }
        Write-Host "✅ Checksum verified: $($actualHash.Substring(0, 16))..." -ForegroundColor Green
    } catch {
        Write-Error "Checksum verification failed: $_"
        throw $_
    }

    # Sigstore / SLSA Build Provenance Attestation Verification
    $requireAttestation = ($env:REQUIRE_ATTESTATION -eq "1")
    $hasGh = [bool](Get-Command gh -ErrorAction SilentlyContinue)

    if ($requireAttestation) {
        if (-not $hasGh) {
            throw "REQUIRE_ATTESTATION=1 is set, but GitHub CLI ('gh') is not installed or not in PATH."
        }
        Write-Host "🔒 Verifying Sigstore SLSA build provenance (strict mode)..." -ForegroundColor Cyan
        & gh attestation verify $tempFile --repo $repo 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "Sigstore SLSA build provenance verification failed for repository $repo (fail-closed)."
        }
        Write-Host "✅ Sigstore SLSA build provenance verified via GitHub CLI" -ForegroundColor Green
    } elseif ($hasGh) {
        & gh attestation verify $tempFile --repo $repo 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Host "✅ Sigstore SLSA build provenance verified via GitHub CLI" -ForegroundColor Green
        }
    }

    Move-Item -Path $tempFile -Destination $targetExe -Force
} catch {
    Write-Error "Failed to install lazylocal: $_"
    if (Test-Path $tempFile) { Remove-Item -Path $tempFile -Force }
    if (Test-Path $tempChecksums) { Remove-Item -Path $tempChecksums -Force }
    exit 1
} finally {
    if (Test-Path $tempChecksums) { Remove-Item -Path $tempChecksums -Force }
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
