param(
    [string]$InstallDir = "$env:LOCALAPPDATA\deepact"
)

$Repo   = "hxs996-beep/deepAct"
$Bin    = "deepact.exe"
$ApiUrl = "https://api.github.com/repos/$Repo/releases/latest"
$Arch   = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "x86" }

if ($Arch -eq "x86") {
    Write-Host "❌ 32-bit Windows is not supported" -ForegroundColor Red
    exit 1
}

Write-Host "📡 Looking up latest release..." -ForegroundColor Cyan
try {
    $Release = Invoke-RestMethod -Uri $ApiUrl -Headers @{ "Accept" = "application/vnd.github.v3+json" }
    $Version = $Release.tag_name
} catch {
    Write-Host "❌ Failed to get latest version: $_" -ForegroundColor Red
    exit 1
}
Write-Host "   Latest: $Version"

$ArchiveName = "deepact_$Version`_windows_$Arch.zip"
$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$ArchiveName"

Write-Host "📥 Downloading $ArchiveName ..." -ForegroundColor Cyan
$TmpZip = Join-Path $env:TEMP $ArchiveName
Invoke-WebRequest -Uri $DownloadUrl -OutFile $TmpZip

Write-Host "📦 Extracting to $InstallDir ..." -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Expand-Archive -Path $TmpZip -DestinationPath $InstallDir -Force

Remove-Item $TmpZip -Force

$BinPath = Join-Path $InstallDir $Bin
if (Test-Path $BinPath) {
    Write-Host "✅ Installed deepact $Version to $InstallDir" -ForegroundColor Green
} else {
    Write-Host "❌ Binary not found after extraction" -ForegroundColor Red
    exit 1
}

# Check if installation dir is in PATH
$UserPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($UserPath -notlike "*$InstallDir*") {
    Write-Host "🔧 Adding $InstallDir to user PATH ..." -ForegroundColor Yellow
    [Environment]::SetEnvironmentVariable("PATH", "$UserPath;$InstallDir", "User")
    Write-Host "   Added. Restart your terminal or run: `$env:PATH += `";$InstallDir`"" -ForegroundColor Gray
} else {
    Write-Host "   $InstallDir already in PATH" -ForegroundColor Gray
}

Write-Host ""
Write-Host "   Run:  deepact" -ForegroundColor Cyan
Write-Host "   Help: deepact --help" -ForegroundColor Cyan
