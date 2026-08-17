[CmdletBinding()]
param([string]$CustomBranch = "custom/upstream-billing")

$scriptPath = Join-Path $PSScriptRoot 'preflight.py'
$python = Get-Command python -ErrorAction SilentlyContinue
if ($python) {
    & $python.Source $scriptPath --custom-branch $CustomBranch
    exit $LASTEXITCODE
}

$launcher = Get-Command py -ErrorAction SilentlyContinue
if ($launcher) {
    & $launcher.Source -3 $scriptPath --custom-branch $CustomBranch
    exit $LASTEXITCODE
}

Write-Error 'Python 3 is required. Install Python 3 or run preflight.py from another environment.'
exit 1
