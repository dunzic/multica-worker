# Offline, per-user installation. No administrator privileges are required.
[CmdletBinding()]
param([switch]$Login)
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

try {
    if ($env:OS -ne "Windows_NT") { throw "This installer requires Windows." }
    if (-not [Environment]::Is64BitOperatingSystem) { throw "64-bit Windows is required." }
    $source = Join-Path $PSScriptRoot "multica.exe"
    $manifest = Get-Content (Join-Path $PSScriptRoot "checksums.txt")
    $entry = @($manifest | Where-Object { $_ -match '^[a-fA-F0-9]{64}\s+\*?multica\.exe$' })
    if ($entry.Count -ne 1) { throw "Missing or ambiguous multica.exe checksum." }
    $expected = ($entry[0] -split '\s+')[0]
    if ((Get-FileHash $source -Algorithm SHA256).Hash -ne $expected) {
        throw "Checksum verification failed. Download the package again from our release channel."
    }
    $bytes = [System.IO.File]::ReadAllBytes($source)
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    $machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
    $nativeArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($machine -eq 0xaa64 -and $nativeArch -ne "ARM64") {
        throw "This ARM64 package requires Windows on ARM. Download the amd64 package."
    }
    if ($machine -ne 0xaa64 -and $machine -ne 0x8664) { throw "Unsupported CLI architecture." }

    $profileDir = Join-Path $env:USERPROFILE ".multica\profiles\turingomni"
    $configPath = Join-Path $profileDir "config.json"
    $service = "https://workers.turingomni.com"
    if (Test-Path $configPath) {
        $config = Get-Content -Raw $configPath | ConvertFrom-Json
        # Do not move existing credentials to a different server.
        if ($config.server_url -ne $service -or $config.app_url -ne $service) {
            throw "The turingomni profile uses another service. Back up and rename that profile before installing."
        }
    }

    $binDir = Join-Path $env:USERPROFILE ".multica\bin"
    New-Item -ItemType Directory -Force $binDir | Out-Null
    Copy-Item $source (Join-Path $binDir "multica.exe") -Force
    $exe = Join-Path $binDir "multica.exe"
    if (-not (Test-Path $configPath)) {
        & $exe --profile turingomni config set server_url $service
        if ($LASTEXITCODE -ne 0) { throw "Could not configure server_url." }
        & $exe --profile turingomni config set app_url $service
        if ($LASTEXITCODE -ne 0) { throw "Could not configure app_url." }
    }
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @($userPath -split ';' | Where-Object { $_ -and $_ -ne $binDir })
    [Environment]::SetEnvironmentVariable("Path", (@($binDir) + $parts) -join ';', "User")
    $env:Path = "$binDir;$env:Path"
    & $exe version
    if ($LASTEXITCODE -ne 0) { throw "Installed CLI could not run. Check the package architecture." }
    Write-Host "Installed for $service. Existing default/private profiles were preserved."
    Write-Host "Open a new terminal, then run: multica --profile turingomni login"
    Write-Host "After login: multica --profile turingomni daemon start"
    if ($Login) {
        & $exe --profile turingomni login
        if ($LASTEXITCODE -ne 0) { throw "Login failed; installation is complete. Retry multica --profile turingomni login." }
    }
} catch {
    Write-Error $_
    exit 1
}
