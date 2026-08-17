$ErrorActionPreference = "Stop"

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  Write-Error "Go 1.22 or newer is required."
}

New-Item -ItemType Directory -Force -Path dist | Out-Null
go test ./... -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$env:CGO_ENABLED = "1"
go build -buildvcs=false -buildmode=c-shared -o dist/cpa-proxy-manager.dll .
Write-Host "Built dist/cpa-proxy-manager.dll"
