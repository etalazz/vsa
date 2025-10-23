# Reproducible VSA vs Token/Leaky bucket baselines
# Usage: pwsh benchmarks/harness/run_baselines.ps1
# Requires: Go 1.22+

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Move to harness directory so `go run .` works
Push-Location (Split-Path -Parent $MyInvocation.MyCommand.Path)
try {
  # Common, reproducible knobs
  $duration = ${env:HARNESS_DURATION}; if (-not $duration) { $duration = '750ms' }
  $workers = ${env:HARNESS_WORKERS}; if (-not $workers) { $workers = 32 }
  $keys = ${env:HARNESS_KEYS}; if (-not $keys) { $keys = 128 }
  $churn = ${env:HARNESS_CHURN}; if (-not $churn) { $churn = 50 }
  $seed = ${env:HARNESS_SEED}; if (-not $seed) { $seed = 1 }

  # VSA knobs
  $threshold = ${env:HARNESS_THRESHOLD}; if (-not $threshold) { $threshold = 192 }
  $low = ${env:HARNESS_LOW_THRESHOLD}; if (-not $low) { $low = 96 }
  $maxAge = ${env:HARNESS_MAX_AGE}; if (-not $maxAge) { $maxAge = '20ms' }
  $commitInterval = ${env:HARNESS_COMMIT_INTERVAL}; if (-not $commitInterval) { $commitInterval = '5ms' }

  # Baseline knobs
  $rate = ${env:HARNESS_RATE}; if (-not $rate) { $rate = 10000 }
  $burst = ${env:HARNESS_BURST}; if (-not $burst) { $burst = 100 }

  # Persistence delay (set to e.g. 50us to reveal differences)
  $writeDelay = ${env:HARNESS_WRITE_DELAY}; if (-not $writeDelay) { $writeDelay = '50us' }

  function Run-Case([string]$variant) {
    $args = @("run", ".",
      "-variant=$variant",
      "-duration=$duration",
      "-goroutines=$workers",
      "-keys=$keys",
      "-churn=$churn",
      "-seed=$seed",
      "-write_delay=$writeDelay",
      "-max_latency_samples=100000",
      "-sample_every=8"
    )
    if ($variant -eq 'vsa') {
      $args += @(
        "-threshold=$threshold",
        "-low_threshold=$low",
        "-commit_max_age=$maxAge",
        "-commit_interval=$commitInterval"
      )
    } elseif ($variant -eq 'token' -or $variant -eq 'leaky') {
      $args += @(
        "-rate=$rate",
        "-burst=$burst"
      )
    }
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = "go"
    $psi.ArgumentList.AddRange($args)
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.UseShellExecute = $false
    $p = [System.Diagnostics.Process]::Start($psi)
    $out = $p.StandardOutput.ReadToEnd()
    $err = $p.StandardError.ReadToEnd()
    $p.WaitForExit()
    if ($p.ExitCode -ne 0) {
      throw "harness failed ($variant): $err`n$out"
    }
    return $out
  }

  function Parse-Metric($text, [string]$pattern, [int]$groupIndex) {
    $m = [System.Text.RegularExpressions.Regex]::Match($text, $pattern, [System.Text.RegularExpressions.RegexOptions]::Multiline)
    if ($m.Success) { return $m.Groups[$groupIndex].Value }
    return $null
  }

  function Parse-Result($text) {
    # Try machine-readable Summary first
    $sumLine = ($text -split "`n") | Where-Object { $_ -match '^Summary:' } | Select-Object -First 1
    if ($sumLine) {
      $fields = @{}
      foreach ($kv in ($sumLine -replace '^Summary:\s+', '') -split '\s+') {
        if ($kv -match '=') {
          $parts = $kv -split '=', 2
          $fields[$parts[0]] = $parts[1]
        }
      }
      $variant = $fields['variant']
      $ops = [int64]$fields['ops']
      $durNs = [double]$fields['duration_ns']
      $durSec = $durNs / 1e9
      $p50us = [double]$fields['p50_ns'] / 1000.0
      $p95us = [double]$fields['p95_ns'] / 1000.0
      $p99us = [double]$fields['p99_ns'] / 1000.0
      $wlog = [int64]$fields['logical_writes']
      $dbc = [int64]$fields['db_calls']
      $opsPerSec = if ($durSec -gt 0) { [math]::Round($ops / $durSec, 1) } else { 0 }
      $opsPerKey = if ($durSec -gt 0 -and ${keys} -gt 0) { [math]::Round(($ops / $durSec) / [double]$keys, 1) } else { 0 }
      $dbcPerSec = if ($durSec -gt 0) { [math]::Round($dbc / $durSec, 1) } else { 0 }
      $wlogPerSec = if ($durSec -gt 0) { [math]::Round($wlog / $durSec, 1) } else { 0 }
      $opsPerDb = if ($dbc -gt 0) { [math]::Round($ops / [double]$dbc, 3) } else { [double]::NaN }
      $durHuman = (Parse-Metric $text '^Duration:\s+([^\s]+)' 1)
      if (-not $durHuman) { $durHuman = "{0:N3}s" -f $durSec }
      return [PSCustomObject]@{
        variant=$variant; ops=$ops; duration=$durHuman; ops_per_sec=$opsPerSec;
        p50_us=('{0:N3}' -f $p50us); p95_us=('{0:N3}' -f $p95us); p99_us=('{0:N3}' -f $p99us);
        logical_writes=$wlog; db_calls=$dbc; ops_per_db=$opsPerDb; ops_per_sec_calc=$opsPerSec;
        ops_per_sec_per_key=$opsPerKey; db_calls_per_sec=$dbcPerSec; logical_writes_per_sec=$wlogPerSec
      }
    }
    # Legacy parsing fallback
    $variant = Parse-Metric $text '^Variant:\s+(\w+)' 1
    $ops = Parse-Metric $text 'Ops:\s+(\d+)' 1
    $dur = Parse-Metric $text '^Duration:\s+([^\s]+)' 1
    $opssec = Parse-Metric $text 'Ops/sec:\s+([0-9,]+)' 1
    $p50 = Parse-Metric $text 'Latency p50:\s+([0-9.]+)µs' 1
    $p95 = Parse-Metric $text 'p95:\s+([0-9.]+)µs' 1
    $p99 = Parse-Metric $text 'p99:\s+([0-9.]+)µs' 1
    $wlog = (Parse-Metric $text 'Writes:\s+logical=([0-9,]+)' 1) -replace ',', ''
    $dbc = (Parse-Metric $text 'dbCalls=([0-9,]+)' 1) -replace ',', ''
    return [PSCustomObject]@{
      variant = $variant; ops = [int64]$ops; duration = $dur; ops_per_sec = $opssec;
      p50_us = $p50; p95_us = $p95; p99_us = $p99; logical_writes = [int64]$wlog; db_calls = [int64]$dbc;
      ops_per_db=$null; ops_per_sec_calc=$null; ops_per_sec_per_key=$null; db_calls_per_sec=$null; logical_writes_per_sec=$null
    }
  }

  $variants = @('token','leaky','vsa')
  $results = @()
  foreach ($v in $variants) {
    Write-Host "Running $v ..." -ForegroundColor Cyan
    $out = Run-Case $v
    # Show the tail of the output for inspection
    Write-Host ($out -split "`n" | Select-Object -Last 12) -ForegroundColor DarkGray
    $results += (Parse-Result $out)
  }

  # Print concise TSV for spreadsheets (includes derived apples-to-apples metrics)
  Write-Host "`nVariant	Ops	Duration	Ops/sec	P50(us)	P95(us)	P99(us)	LogicalWrites	DBCalls	OpsPerDBCall	Ops/sec(calc)	Ops/sec/key	DBCalls/sec	LogicalWrites/sec" -ForegroundColor Green
  foreach ($r in $results) {
    Write-Host ("{0}`t{1}`t{2}`t{3}`t{4}`t{5}`t{6}`t{7}`t{8}`t{9}`t{10}`t{11}`t{12}`t{13}" -f $r.variant,$r.ops,$r.duration,$r.ops_per_sec,$r.p50_us,$r.p95_us,$r.p99_us,$r.logical_writes,$r.db_calls,$r.ops_per_db,$r.ops_per_sec_calc,$r.ops_per_sec_per_key,$r.db_calls_per_sec,$r.logical_writes_per_sec)
  }
}
finally {
  Pop-Location
}
