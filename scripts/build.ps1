# 本机出 Windows GUI 子系统产物（双击无黑窗；CLI 打印靠 AttachConsole 附着父控制台）。
# 产物只落 build/（已 gitignore），不入库。
$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $MyInvocation.MyCommand.Path | ForEach-Object { Split-Path -Parent $_ })

if (-not (Test-Path "build")) { New-Item -ItemType Directory -Path "build" | Out-Null }

Write-Host "==> windows/amd64"
$env:CGO_ENABLED = "0"
$env:GOFLAGS = "-mod=mod"
go build -buildvcs=false -trimpath -ldflags "-s -w -H windowsgui" -o "build/archify-viewer-windows-amd64.exe" .
if ($LASTEXITCODE -ne 0) { throw "go build 失败：$LASTEXITCODE" }

Write-Host "==> done: build/archify-viewer-windows-amd64.exe"
& ".\build\archify-viewer-windows-amd64.exe" version
