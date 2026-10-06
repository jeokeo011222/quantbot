# QuantBot Build Script - NO -clean flag. Preserves build\bin data/config/log.
# Usage: .\build.ps1
# Note: wails build WITHOUT -clean does not delete existing dirs under build\bin,
#       so databases, configs and logs are fully preserved. No backup needed.

$ErrorActionPreference = "Stop"

$ProjectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$BuildBin = Join-Path $ProjectRoot "build\bin"
$OutputExe = Join-Path $BuildBin "QuantBot.exe"

Write-Host "============================================"
Write-Host "  QuantBot Build Script"
Write-Host "============================================"
Write-Host ""

# 1. Environment check (explicit paths, independent of terminal PATH)
$goExe = "D:\go\bin\go.exe"
$wailsExe = "C:\Users\JokerZ\go\bin\wails.exe"
if (-not (Test-Path $goExe)) {
    Write-Host "[ERROR] Go not found: $goExe" -ForegroundColor Red
    exit 1
}
if (-not (Test-Path $wailsExe)) {
    Write-Host "[ERROR] Wails CLI not found: $wailsExe (run: go install github.com/wailsapp/wails/v2/cmd/wails@latest)" -ForegroundColor Red
    exit 1
}
Write-Host "[OK] Go: $goExe"
Write-Host "[OK] Wails: $wailsExe"
Write-Host ""

# 2. Set Go environment variables (consistent with dev environment)
$env:GOROOT = "D:\go"
$env:GOPATH = "C:\Users\JokerZ\go"
$env:PATH = "D:\go\bin;$env:GOPATH\bin;$env:PATH"
Write-Host "[Env] GOROOT=$env:GOROOT"
Write-Host "[Env] GOPATH=$env:GOPATH"
Write-Host ""

# 3. Verify protected dirs under build\bin (informational)
$ProtectedDirs = @("data", "config", "log", "database", "models")
$missing = @()
foreach ($dir in $ProtectedDirs) {
    $p = Join-Path $BuildBin $dir
    if (Test-Path $p) {
        Write-Host "  [OK] preserved: $dir" -ForegroundColor Green
    } else {
        $missing += $dir
    }
}
if ($missing.Count -gt 0) {
    Write-Host "  [warn] not present under build\bin: $($missing -join ', ')" -ForegroundColor Yellow
}
Write-Host ""

# 4. Run wails build (NO -clean; keeps build\bin databases and configs)
Write-Host "[Build] Starting wails build (wails build -platform windows/amd64)..." -ForegroundColor Cyan
Push-Location $ProjectRoot
try {
    & $wailsExe build -platform windows/amd64
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[ERROR] wails build failed (exit code: $LASTEXITCODE)" -ForegroundColor Red
        exit 1
    }
    Write-Host "  Build successful!" -ForegroundColor Green
} finally {
    Pop-Location
}

# 5. Verify output
Write-Host ""
if (-not (Test-Path $OutputExe)) {
    Write-Host "[ERROR] Output not found: $OutputExe" -ForegroundColor Red
    exit 1
}
$sizeMB = [math]::Round((Get-Item $OutputExe).Length / 1MB, 1)
Write-Host "[OK] Output: $OutputExe ($sizeMB MB)" -ForegroundColor Green

# 6. Copy decision-brain DLL next to the exe (bin\agent.dll -> build\bin\agent.dll)
#    The exe looks for agent.dll in its own directory by default; without this
#    copy it silently falls back to harness instead of using the DLL.
#    NOTE: keep this block ASCII-only - build.ps1 may run under Windows
#    PowerShell 5.1 which mis-reads non-BOM UTF-8 and breaks parsing.
$SrcDll = Join-Path $ProjectRoot "bin\agent.dll"
$DstDll = Join-Path $BuildBin "agent.dll"
if (Test-Path $SrcDll) {
    Copy-Item -Path $SrcDll -Destination $DstDll -Force
    $dllMB = [math]::Round((Get-Item $DstDll).Length / 1MB, 2)
    Write-Host "[OK] Copied agent.dll -> $DstDll ($dllMB MB)" -ForegroundColor Green
    $SrcH = Join-Path $ProjectRoot "bin\agent.h"
    if (Test-Path $SrcH) {
        Copy-Item -Path $SrcH -Destination (Join-Path $BuildBin "agent.h") -Force
        Write-Host "[OK] Copied agent.h -> $BuildBin\agent.h" -ForegroundColor Green
    }
} else {
    Write-Host "[warn] bin\agent.dll not found; this exe will fall back to harness (no agent.dll decision brain)" -ForegroundColor Yellow
}

# 7. Sync bridge strategy folders from third_party into build\bin.
#    (Ptrade / XtQuant ship with every build: bridge scripts + manuals)
$BridgePairs = @(
    @{ Src = "third_party\Ptrade";  Dst = "Ptrade" },
    @{ Src = "third_party\XtQuant"; Dst = "XtQuant" }
)
foreach ($bp in $BridgePairs) {
    $src = Join-Path $ProjectRoot $bp.Src
    $dst = Join-Path $BuildBin $bp.Dst
    if (Test-Path $src) {
        if (-not (Test-Path $dst)) { New-Item -ItemType Directory -Path $dst -Force | Out-Null }
        Copy-Item -Path (Join-Path $src '*') -Destination $dst -Force -Recurse
        Write-Host "[OK] Synced $($bp.Src) -> build\bin\$($bp.Dst)" -ForegroundColor Green
    } else {
        Write-Host "[warn] $($bp.Src) not found; bridge scripts not synced" -ForegroundColor Yellow
    }
}

Write-Host ""
Write-Host "============================================"
Write-Host "  Build complete! (build\bin data fully preserved)"
Write-Host "============================================"
