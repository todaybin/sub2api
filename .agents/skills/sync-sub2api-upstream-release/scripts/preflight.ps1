[CmdletBinding()]
param([string]$CustomBranch = "custom/upstream-billing")

$ErrorActionPreference = "Stop"

function Invoke-Git {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $output = & git @Arguments 2>&1
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = $previousErrorAction
    $output = @($output | ForEach-Object { $_.ToString() } | Where-Object {
        $_ -notmatch "^warning: unable to access '.+[/\\]\.config[/\\]git[/\\]ignore': Permission denied$"
    })
    if ($exitCode -ne 0) {
        throw "git $($Arguments -join ' ') failed: $($output -join [Environment]::NewLine)"
    }
    return $output
}

function Get-RemoteUrl([string]$Remote) {
    return ((Invoke-Git remote get-url $Remote | Select-Object -First 1).Trim())
}

function Get-RepositoryIdentity([string]$Url) {
    $identity = $Url.Trim() -replace '\\', '/'
    $identity = $identity -replace '^git@([^:]+):', '$1/'
    $identity = $identity -replace '^[a-zA-Z][a-zA-Z0-9+.-]*://', ''
    return (($identity.TrimEnd('/') -replace '\.git$', '').ToLowerInvariant())
}

function Test-GitRef([string]$Ref) {
    & git show-ref --verify --quiet $Ref
    return $LASTEXITCODE -eq 0
}

function Get-OfficialBranch {
    $symbolic = & git symbolic-ref --quiet --short refs/remotes/upstream/HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $symbolic) {
        return ($symbolic.Trim() -replace '^upstream/', '')
    }
    foreach ($candidate in @('main', 'master')) {
        if (Test-GitRef "refs/remotes/upstream/$candidate") { return $candidate }
    }
    throw "Cannot determine upstream default branch."
}

function Get-MergeState {
    $gitDir = (Invoke-Git rev-parse --git-dir | Select-Object -First 1).Trim()
    if (-not [System.IO.Path]::IsPathRooted($gitDir)) { $gitDir = Join-Path (Get-Location) $gitDir }
    return [pscustomobject][ordered]@{
        merge       = Test-Path (Join-Path $gitDir 'MERGE_HEAD')
        rebase      = (Test-Path (Join-Path $gitDir 'rebase-merge')) -or (Test-Path (Join-Path $gitDir 'rebase-apply'))
        cherry_pick = Test-Path (Join-Path $gitDir 'CHERRY_PICK_HEAD')
        revert      = Test-Path (Join-Path $gitDir 'REVERT_HEAD')
    }
}

function Get-BaseVersion([string]$RepositoryRoot) {
    $versionFile = Join-Path $RepositoryRoot 'backend/cmd/server/VERSION'
    if (-not (Test-Path -LiteralPath $versionFile)) { throw "Missing version file: $versionFile" }
    $current = (Get-Content -LiteralPath $versionFile -Raw).Trim()
    $match = [regex]::Match($current, '^(?<base>\d+\.\d+\.\d+)(?:-custom\.\d+)?$')
    if (-not $match.Success) { throw "Unsupported VERSION value: $current" }
    return [pscustomobject]@{ current = $current; base = $match.Groups['base'].Value }
}

function Get-NextCustomVersion([string]$BaseVersion) {
    $escapedBase = [regex]::Escape($BaseVersion)
    $numbers = [System.Collections.Generic.List[int]]::new()
    foreach ($tag in @(Invoke-Git tag --list "v$BaseVersion-custom.*")) {
        $match = [regex]::Match($tag.Trim(), "^v$escapedBase-custom\.(\d+)$")
        if ($match.Success) { $numbers.Add([int]$match.Groups[1].Value) }
    }
    $gitPath = (Get-Command git -ErrorAction Stop).Source
    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $gitPath
    $startInfo.WorkingDirectory = (Get-Location).Path
    $startInfo.Arguments = "-c http.lowSpeedLimit=1 -c http.lowSpeedTime=10 ls-remote --tags origin refs/tags/v$BaseVersion-custom.*"
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $null = $process.Start()
    if (-not $process.WaitForExit(20000)) {
        $process.Kill()
        throw "Querying origin tags timed out after 20 seconds."
    }
    $remoteOutput = $process.StandardOutput.ReadToEnd()
    $remoteError = $process.StandardError.ReadToEnd().Trim()
    if ($process.ExitCode -ne 0) {
        throw "Unable to query origin tags: $remoteError"
    }
    foreach ($line in @($remoteOutput -split "`r?`n" | Where-Object { $_ })) {
        $match = [regex]::Match($line, "refs/tags/v$escapedBase-custom\.(\d+)(?:\^\{\})?$")
        if ($match.Success) { $numbers.Add([int]$match.Groups[1].Value) }
    }
    $nextNumber = if ($numbers.Count -eq 0) { 1 } else { (($numbers | Measure-Object -Maximum).Maximum + 1) }
    return [pscustomobject]@{
        version = "$BaseVersion-custom.$nextNumber"
        tag = "v$BaseVersion-custom.$nextNumber"
    }
}

$repositoryRoot = (Invoke-Git rev-parse --show-toplevel | Select-Object -First 1).Trim()
Set-Location -LiteralPath $repositoryRoot
$upstreamUrl = Get-RemoteUrl 'upstream'
$originUrl = Get-RemoteUrl 'origin'
$upstreamIdentity = Get-RepositoryIdentity $upstreamUrl
$originIdentity = Get-RepositoryIdentity $originUrl

if ($upstreamIdentity -notmatch '(^|/)github\.com/wei-shaw/sub2api$') {
    throw "upstream must point to Wei-Shaw/sub2api; found: $upstreamUrl"
}
if ($originIdentity -eq $upstreamIdentity) { throw "origin and upstream resolve to the same repository." }

$officialBranch = Get-OfficialBranch
if (-not (Test-GitRef "refs/heads/$CustomBranch")) { throw "Custom branch does not exist locally: $CustomBranch" }

$branch = (Invoke-Git branch --show-current | Select-Object -First 1).Trim()
$statusLines = @(Invoke-Git status --short --untracked-files=normal)
$version = Get-BaseVersion $repositoryRoot
$next = Get-NextCustomVersion $version.base
$originCustomHead = $null
if (Test-GitRef "refs/remotes/origin/$CustomBranch") {
    $originCustomHead = (Invoke-Git rev-parse "refs/remotes/origin/$CustomBranch" | Select-Object -First 1).Trim()
}

[ordered]@{
    repository_root = $repositoryRoot
    remotes = [ordered]@{ upstream = $upstreamUrl; origin = $originUrl }
    branch = [ordered]@{
        current = $branch
        expected_custom = $CustomBranch
        head = (Invoke-Git rev-parse HEAD | Select-Object -First 1).Trim()
        origin_custom_head = $originCustomHead
        official = $officialBranch
        upstream_official_head = (Invoke-Git rev-parse "refs/remotes/upstream/$officialBranch" | Select-Object -First 1).Trim()
    }
    working_tree = [ordered]@{
        clean = $statusLines.Count -eq 0
        tracked_changes = @($statusLines | Where-Object { $_ -notmatch '^\?\?' })
        untracked_changes = @($statusLines | Where-Object { $_ -match '^\?\?' })
    }
    operation_in_progress = Get-MergeState
    version = [ordered]@{
        current_version = $version.current
        official_base = $version.base
        proposed_version = $next.version
        proposed_tag = $next.tag
    }
    stashes = @(Invoke-Git stash list)
    note = 'Read-only snapshot. Fetch upstream/origin before relying on commit or tag values.'
} | ConvertTo-Json -Depth 6
