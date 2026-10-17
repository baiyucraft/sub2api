param(
  [Parameter(Mandatory = $true)]
  [ValidateSet('smoke', 'refresh-cpu', 'native-scroll', 'stability-start', 'short-stability-start', 'stability-read', 'close')]
  [string]$Scenario,
  [string]$Session = 'upstream-table-browser'
)
$ErrorActionPreference = 'Stop'
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../../../../../../'))
$scriptPath = $PSScriptRoot
function Invoke-Browser([string[]]$BrowserArgs) {
  $browserOutput = & npx --yes --package '@playwright/cli@0.1.22' playwright-cli "-s=$Session" @BrowserArgs
  $browserExitCode = $LASTEXITCODE
  $browserOutput | Write-Output
  # The CLI can print a script error while returning exit code zero.
  if ($browserExitCode -ne 0 -or ($browserOutput -join "`n") -match '(?m)^### Error\b') {
    throw "Browser command failed: $($BrowserArgs[0])"
  }
}
Push-Location $repo
$startedBrowser = $false
$stabilityStarted = $false
try {
  if ($Scenario -eq 'close') {
    Invoke-Browser -BrowserArgs @('close')
  } elseif ($Scenario -eq 'stability-read') {
    Invoke-Browser -BrowserArgs @('run-code', '--filename', (Join-Path $scriptPath 'stability-read.js'))
  } else {
    # Each measurement owns a fresh renderer; never share it with a stability run.
    Invoke-Browser -BrowserArgs @('close')
    Invoke-Browser -BrowserArgs @('open', 'http://127.0.0.1:4319')
    $startedBrowser = $true
    if ($Scenario -in @('smoke', 'stability-start', 'short-stability-start')) {
      Invoke-Browser -BrowserArgs @('run-code', '--filename', (Join-Path $scriptPath 'automatic-mode.js'))
    }
    Invoke-Browser -BrowserArgs @('run-code', '--filename', (Join-Path $scriptPath 'fixture.js'))
    Invoke-Browser -BrowserArgs @('run-code', '--filename', (Join-Path $scriptPath 'runtime-info.js'))
    Invoke-Browser -BrowserArgs @('run-code', '--filename', (Join-Path $scriptPath "$Scenario.js"))
    $stabilityStarted = $Scenario -in @('stability-start', 'short-stability-start')
  }
} finally {
  if ($startedBrowser -and -not $stabilityStarted) { Invoke-Browser -BrowserArgs @('close') }
  Pop-Location
}
