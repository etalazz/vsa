# Copyright 2025 Esteban Alvarez. All Rights Reserved.
#
# Apache-2.0 License
#
# Proxy smoke test for plugin/tfd: launches cmd/tfd-proxy, sends a few
# requests (/consume, /reverse, /set_limit), and asserts reconstructed state.
#
# Usage (PowerShell):
#   ./proxy_smoke_test.ps1 [-Port 9090] [-Key 'user:1'] [-Bucket 't1s/001']
#
# The script builds the proxy binary, starts it on the chosen port, verifies
# /healthz, sends traffic, checks /state?key=..., and then shuts down the proxy.

param(
  [int]$Port = 9090,
  [string]$Key = 'user:1',
  [string]$Bucket = 't1s/001'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-Info($msg) { Write-Host "[INFO] $msg" -ForegroundColor Cyan }
function Write-Err($msg)  { Write-Host "[ERR ] $msg" -ForegroundColor Red }
function Assert-True($cond, $msg) {
  if (-not $cond) { throw "ASSERT FAILED: $msg" }
}

# Resolve repository root (this file is under plugin/tfd/scripts)
$repoRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
$binDir   = Join-Path $repoRoot 'bin'
$proxyBin = Join-Path $binDir 'tfd-proxy.exe'

# Temp logs directory (kept for inspection)
$stamp    = (Get-Date).ToString('yyyyMMdd-HHmmss')
$tmpDir   = Join-Path $PSScriptRoot ".tmp-$stamp"
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
$sLogPath = Join-Path $tmpDir 's.log'
$vLogPath = Join-Path $tmpDir 'v.log'

# Build proxy
Write-Info "Building tfd-proxy..."
Push-Location $repoRoot
try {
  if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Force -Path $binDir | Out-Null }
  & go build -o $proxyBin .\cmd\tfd-proxy  | Out-Null
} finally {
  Pop-Location
}
Assert-True (Test-Path $proxyBin) "Proxy binary not found at $proxyBin"

# Start proxy
Write-Info "Starting tfd-proxy on :$Port (s_log=$sLogPath, v_log=$vLogPath)..."
$startInfo = @{ FilePath = $proxyBin; ArgumentList = @('-http', ":$Port", '-s_log', $sLogPath, '-v_log', $vLogPath, '-flush', '2ms'); PassThru = $true; WindowStyle = 'Hidden' }
$proc = Start-Process @startInfo

$stopProxy = {
  if ($proc -and -not $proc.HasExited) {
    try { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue } catch {}
    try { Wait-Process -Id $proc.Id -Timeout 5 -ErrorAction SilentlyContinue } catch {}
  }
}

try {
  # Wait for healthz
  Write-Info "Waiting for /healthz..."
  $healthy = $false
  for ($i = 0; $i -lt 100; $i++) {
    try {
      $h = Invoke-RestMethod -Uri "http://localhost:$Port/healthz" -TimeoutSec 1
      if ($h.ok) { $healthy = $true; break }
    } catch { }
    Start-Sleep -Milliseconds 100
  }
  Assert-True $healthy "Proxy did not become healthy on port $Port"

  # /consume (S)
  Write-Info "POST /consume (+3)"
  $resp1 = Invoke-RestMethod -Method Post -Uri "http://localhost:$Port/consume?key=$([uri]::EscapeDataString($Key))&bucket=$([uri]::EscapeDataString($Bucket))&n=3" -TimeoutSec 5
  Assert-True ($resp1.accepted -eq $true) "/consume not accepted"
  Assert-True ($resp1.channel -eq 'S') "/consume should route to S, got '$($resp1.channel)'"

  # /reverse (V)
  Write-Info "POST /reverse (-1)"
  $resp2 = Invoke-RestMethod -Method Post -Uri "http://localhost:$Port/reverse?key=$([uri]::EscapeDataString($Key))&bucket=$([uri]::EscapeDataString($Bucket))&n=1" -TimeoutSec 5
  Assert-True ($resp2.accepted -eq $true) "/reverse not accepted"

  # /set_limit (V; policy change)
  Write-Info "POST /set_limit (rps=500)"
  $resp3 = Invoke-RestMethod -Method Post -Uri "http://localhost:$Port/set_limit?key=$([uri]::EscapeDataString($Key))&rps=500" -TimeoutSec 5
  Assert-True ($resp3.accepted -eq $true) "/set_limit not accepted"

  # Allow S-lane to flush
  Start-Sleep -Milliseconds 20

  # /state?key=...
  Write-Info "GET /state?key=$Key"
  $stateRaw = Invoke-WebRequest -Uri "http://localhost:$Port/state?key=$([uri]::EscapeDataString($Key))" -TimeoutSec 5
  $state    = $stateRaw.Content | ConvertFrom-Json

  function Get-MapValues($obj) {
    if ($null -eq $obj) { return @() }
    if ($obj -is [System.Collections.IDictionary]) { return $obj.Values }
    if ($obj.PSObject -and $obj.PSObject.Properties) { return @($obj.PSObject.Properties | ForEach-Object { $_.Value }) }
    return @()
  }
  $vals = Get-MapValues $state
  $sum  = 0
  foreach ($v in $vals) { $sum += [int64]$v }

  Write-Info "Reconstructed sum for key '$Key' = $sum (expected 2)"
  Assert-True ($sum -eq 2) "Reconstructed state mismatch: expected 2, got $sum"

  # Check logs exist and are non-empty
  Assert-True (Test-Path $sLogPath) "S log not found at $sLogPath"
  Assert-True (Test-Path $vLogPath) "V log not found at $vLogPath"
  $sLen = (Get-Item $sLogPath).Length
  $vLen = (Get-Item $vLogPath).Length
  Assert-True ($sLen -gt 0) "S log is empty"
  Assert-True ($vLen -gt 0) "V log is empty"

  Write-Host "OK: proxy smoke test passed." -ForegroundColor Green
  exit 0
}
catch {
  Write-Err $_
  exit 1
}
finally {
  & $stopProxy
}
