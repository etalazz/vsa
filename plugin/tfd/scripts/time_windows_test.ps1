# Copyright 2025 Esteban Alvarez. All Rights Reserved.
#
# Apache-2.0 License
#
# Time windows test for plugin/tfd proxy: asserts per-bucket sums and total.
# It launches cmd/tfd-proxy, sends S and V operations across two buckets, and
# verifies reconstructed state using the /state?key=...&bucket=...&sum=1 endpoint.
#
# Usage (PowerShell):
#   ./time_windows_test.ps1 [-Port 9093] [-Key 'user:tw'] [-Bucket1 't1s/001'] [-Bucket2 't1s/002']

param(
  [int]$Port = 9093,
  [string]$Key = 'user:tw',
  [string]$Bucket1 = 't1s/001',
  [string]$Bucket2 = 't1s/002'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-Info($msg) { Write-Host "[INFO] $msg" -ForegroundColor Cyan }
function Write-Err($msg)  { Write-Host "[ERR ] $msg" -ForegroundColor Red }
function Assert-True($cond, $msg) { if (-not $cond) { throw "ASSERT FAILED: $msg" } }

# Resolve repo paths (this file is under plugin/tfd/scripts)
$repoRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
$binDir   = Join-Path $repoRoot 'bin'
$proxyBin = Join-Path $binDir 'tfd-proxy.exe'

# Temp logs directory
$stamp  = (Get-Date).ToString('yyyyMMdd-HHmmss')
$tmpDir = Join-Path $PSScriptRoot ".tmp-$stamp"
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
$sLogPath = Join-Path $tmpDir 's.log'
$vLogPath = Join-Path $tmpDir 'v.log'

# Build proxy
Write-Info "Building tfd-proxy..."
Push-Location $repoRoot
try {
  if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Force -Path $binDir | Out-Null }
  & go build -o $proxyBin .\cmd\tfd-proxy  | Out-Null
} finally { Pop-Location }
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

  # Scenario across two windows (buckets)
  # Bucket1: +5 (expect 5)
  # Bucket2: +7 then reverse -2 (expect 5)
  $ek = [uri]::EscapeDataString($Key)
  $eb1 = [uri]::EscapeDataString($Bucket1)
  $eb2 = [uri]::EscapeDataString($Bucket2)

  Write-Info "POST /consume key=$Key bucket=$Bucket1 (+5)"
  $r1 = Invoke-RestMethod -Method Post -Uri "http://localhost:$Port/consume?key=$ek&bucket=$eb1&n=5" -TimeoutSec 5
  Assert-True ($r1.accepted -eq $true) "/consume b1 not accepted"

  Write-Info "POST /consume key=$Key bucket=$Bucket2 (+7)"
  $r2 = Invoke-RestMethod -Method Post -Uri "http://localhost:$Port/consume?key=$ek&bucket=$eb2&n=7" -TimeoutSec 5
  Assert-True ($r2.accepted -eq $true) "/consume b2 not accepted"

  Write-Info "POST /reverse key=$Key bucket=$Bucket2 (-2)"
  $r3 = Invoke-RestMethod -Method Post -Uri "http://localhost:$Port/reverse?key=$ek&bucket=$eb2&n=2" -TimeoutSec 5
  Assert-True ($r3.accepted -eq $true) "/reverse b2 not accepted"

  # Wait for S-lane flush
  Start-Sleep -Milliseconds 30

  # Assert per-bucket sums using /state sum=1 & bucket filter
  Write-Info "GET /state?key=$Key&bucket=$Bucket1&sum=1 (expect 5)"
  $s1 = Invoke-RestMethod -Uri "http://localhost:$Port/state?key=$ek&bucket=$eb1&sum=1" -TimeoutSec 5
  Assert-True ($s1.sum -eq 5) "Bucket1 sum mismatch: expected 5, got $($s1.sum)"

  Write-Info "GET /state?key=$Key&bucket=$Bucket2&sum=1 (expect 5)"
  $s2 = Invoke-RestMethod -Uri "http://localhost:$Port/state?key=$ek&bucket=$eb2&sum=1" -TimeoutSec 5
  Assert-True ($s2.sum -eq 5) "Bucket2 sum mismatch: expected 5, got $($s2.sum)"

  # Assert total sum for key
  Write-Info "GET /state?key=$Key&sum=1 (expect 10)"
  $st = Invoke-RestMethod -Uri "http://localhost:$Port/state?key=$ek&sum=1" -TimeoutSec 5
  Assert-True ($st.sum -eq 10) "Total sum mismatch: expected 10, got $($st.sum)"

  # Logs exist and non-empty
  Assert-True (Test-Path $sLogPath) "S log not found at $sLogPath"
  Assert-True (Test-Path $vLogPath) "V log not found at $vLogPath"
  $sLen = (Get-Item $sLogPath).Length
  $vLen = (Get-Item $vLogPath).Length
  Assert-True ($sLen -gt 0) "S log is empty"
  Assert-True ($vLen -gt 0) "V log is empty"

  Write-Host "OK: time windows test passed." -ForegroundColor Green
  exit 0
}
catch {
  Write-Err $_
  exit 1
}
finally {
  & $stopProxy
}
