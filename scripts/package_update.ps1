# QuantBot 升级包打包脚本
# 用法:
#   1) 安全包（公开发布用）: .\scripts\package_update.ps1 [-Version 1.2.0]
#      打包内容: QuantBot.exe + models\* + data\stock_dict.json
#      排除: config database log data\trades data\*.duckdb XtQuant 用户手册.md 等隐私/大文件
#   2) 全量包（含隐私，仅本地备份用，禁止上传公开仓库）:
#      .\scripts\package_update.ps1 -IncludeAll
#      打包内容: build\bin 下全部内容（含数据库/交易记录/配置/1.4GB行情库）
# 产出: dist\QuantBot-v<version>-full.zip / dist\QuantBot-v<version>.zip + 对应 .sha256
param(
    [string]$Version = "",
    [switch]$IncludeAll
)

$ErrorActionPreference = "Stop"

$ProjectRoot = Split-Path -Parent $PSScriptRoot
$BuildBin = Join-Path $ProjectRoot "build\bin"
$DistDir = Join-Path $ProjectRoot "dist"

# 1. 确定版本号：优先 -Version 参数，否则从 internal/version/version.go 读取
if ([string]::IsNullOrEmpty($Version)) {
    $versionFile = Join-Path $ProjectRoot "internal\version\version.go"
    if (-not (Test-Path $versionFile)) {
        Write-Host "[ERROR] 未指定 -Version，且找不到 $versionFile" -ForegroundColor Red
        exit 1
    }
    $content = Get-Content $versionFile -Raw
    if ($content -match 'Version\s*=\s*"([^"]+)"') {
        $Version = $Matches[1]
    } else {
        Write-Host "[ERROR] 无法从 version.go 解析版本号" -ForegroundColor Red
        exit 1
    }
}
if (-not ($Version -match '^\d+\.\d+\.\d+$')) {
    Write-Host "[ERROR] 版本号格式非法（需 major.minor.patch）: $Version" -ForegroundColor Red
    exit 1
}
Write-Host "============================================"
Write-Host "  QuantBot Update Package v$Version"
Write-Host "============================================"

# 2. 校验主程序存在
$Exe = Join-Path $BuildBin "QuantBot.exe"
if (-not (Test-Path $Exe)) {
    Write-Host "[ERROR] 主程序不存在: $Exe（请先运行 build.ps1）" -ForegroundColor Red
    exit 1
}

# 3. 收集待打包文件（相对 build\bin 的路径）
$Files = @()
if ($IncludeAll) {
    # ---- 全量包：build\bin 下全部内容（含隐私数据），仅限本地备份 ----
    Write-Host "[WARN] 全量包模式：将包含数据库/交易记录/配置文件等隐私数据！" -ForegroundColor Yellow
    Write-Host "[WARN] 此包仅可用于本地备份，严禁上传公开仓库！" -ForegroundColor Yellow
    $all = Get-ChildItem -Path $BuildBin -Recurse -File
    foreach ($f in $all) {
        $rel = [System.IO.Path]::GetRelativePath($BuildBin, $f.FullName)
        if ($rel -eq "QuantBot.exe") {
            $Files += $f.FullName
            continue
        }
        if ($rel.StartsWith("dist")) { continue } # 避免包含输出目录
        $Files += $f.FullName
    }
} else {
    # ---- 安全包：仅发布产物 + 版本化数据 ----
    # 模型含全部重训后的 5 个（xgboost/lgbm/rf/logistic/mlp），每个含 json/info/scaler/pkl
    $modelNames = @(
        "xgboost_astock_v1",
        "astock_lgbm_v1",
        "astock_rf_v1",
        "astock_logistic_v1",
        "astock_mlp_v1"
    )
    $Include = @("QuantBot.exe")
    foreach ($mn in $modelNames) {
        foreach ($sfx in @(".json", "_info.json", "_scaler.json", "_scaler.pkl")) {
            $Include += "models\$mn$sfx"
        }
    }
    $Include += "data\stock_dict.json"
    foreach ($rel in $Include) {
        $p = Join-Path $BuildBin $rel
        if (-not (Test-Path $p)) {
            Write-Host "[warn] 文件不存在，跳过: $rel" -ForegroundColor Yellow
            continue
        }
        $Files += $p
    }
}
if ($Files.Count -eq 0) {
    Write-Host "[ERROR] 无任何文件可打包" -ForegroundColor Red
    exit 1
}

# 4. 清理并准备 staging 目录
$Staging = Join-Path $DistDir "staging-v$Version"
if (Test-Path $Staging) { Remove-Item -Recurse -Force $Staging }
New-Item -ItemType Directory -Path $Staging -Force | Out-Null

Write-Host "[Pack] 收集文件到 staging..." -ForegroundColor Cyan
$ManifestFiles = @()
foreach ($src in $Files) {
    $rel = [System.IO.Path]::GetRelativePath($BuildBin, $src)
    $dst = Join-Path $Staging $rel
    New-Item -ItemType Directory -Path (Split-Path -Parent $dst) -Force | Out-Null
    Copy-Item $src $dst -Force
    # 计算 SHA256 与大小
    $hash = (Get-FileHash -Algorithm SHA256 $dst).Hash.ToLower()
    $size = (Get-Item $dst).Length
    $ManifestFiles += [pscustomobject]@{
        path = $rel.Replace('\', '/')
        size = $size
        sha256 = $hash
    }
    Write-Host "  [OK] $rel ($([math]::Round($size / 1MB, 2)) MB)" -ForegroundColor Green
}

# 5. 生成 manifest.json（min_version 取当前版本，向上兼容）
$Manifest = [ordered]@{
    version     = $Version
    min_version = "1.0.0"
    files       = @($ManifestFiles)
}
$ManifestPath = Join-Path $Staging "manifest.json"
$Manifest | ConvertTo-Json -Depth 5 | Set-Content -Path $ManifestPath -Encoding UTF8
Write-Host "  [OK] manifest.json ($(($Manifest.files | Measure-Object).Count) 个文件)" -ForegroundColor Green

# 6. 打包 zip（不含 staging 目录本身）
$suffix = if ($IncludeAll) { "-full" } else { "" }
$ZipName = "QuantBot-v$Version$suffix.zip"
$ZipPath = Join-Path $DistDir $ZipName
if (Test-Path $ZipPath) { Remove-Item -Force $ZipPath }
Write-Host "[Pack] 压缩: $ZipPath" -ForegroundColor Cyan
Compress-Archive -Path (Join-Path $Staging "*") -DestinationPath $ZipPath -CompressionLevel Optimal

# 7. 生成 zip 整体 SHA256 校验文件
$ZipHash = (Get-FileHash -Algorithm SHA256 $ZipPath).Hash.ToLower()
$ShaPath = "$ZipPath.sha256"
"$ZipHash  $ZipName" | Set-Content -Path $ShaPath -Encoding ASCII
$ZipMB = [math]::Round((Get-Item $ZipPath).Length / 1MB, 2)
Write-Host ""
Write-Host "  [OK] $ZipName ($ZipMB MB)" -ForegroundColor Green
Write-Host "  [OK] $ZipName.sha256" -ForegroundColor Green

# 8. 清理 staging
Remove-Item -Recurse -Force $Staging

# 9. 输出发布指引
Write-Host ""
Write-Host "============================================"
Write-Host "  打包完成！发布到 GitHub Releases："
Write-Host "============================================"
Write-Host "  git tag v$Version"
Write-Host "  git push origin v$Version"
Write-Host "  gh release create v$Version `"$ZipPath`" `"$ShaPath`" --notes `"更新日志...`""
Write-Host ""
Write-Host "  zip  SHA256: $ZipHash"
