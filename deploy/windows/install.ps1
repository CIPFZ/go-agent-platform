<#
.SYNOPSIS
  Install or upgrade vm-agent as a logon scheduled task of the current user.

.DESCRIPTION
  Run as the desktop user that logs in automatically (spec 4.1). The binary
  is copied to %LOCALAPPDATA%\vm-agent\releases\<Version>, the "current"
  junction is switched to it and the previous target is kept as "previous"
  for rollback.ps1. On first install config\vm-agent.args is written from the
  parameters; later installs keep it unless -Reconfigure is given, so an
  upgrade and a rollback never lose the configuration or anything under
  state\ (audit log, jobs, recordings).

  -GenerateAuthToken makes vm-agent require an access token (docs/security.md).
  The token is stored in config\auth-token, readable by this user, SYSTEM
  and administrators like the rest of %LOCALAPPDATA%; replace the file's
  content to change the token, remove the -auth-token-file line from
  config\vm-agent.args and reinstall to stop requiring one.

  -RunLevel Highest runs vm-agent elevated so it can send input to elevated
  windows; it needs an administrator account and, to avoid prompts,
  ConsentPromptBehaviorAdmin=0 (preconditions.ps1).

.EXAMPLE
  .\install.ps1 -Binary .\vm-agent-windows-amd64.exe -Version 1.2.0
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Binary,
    [string]$Version = (Get-Date -Format 'yyyyMMddHHmmss'),
    [string]$Listen = '0.0.0.0:7070',
    [string]$ExtraArgs = '',
    [ValidateSet('Limited', 'Highest')][string]$RunLevel = 'Limited',
    [string]$TaskName = 'vm-agent',
    [switch]$Reconfigure,
    # Require an access token: creates config\auth-token with a random token
    # (unless the file exists) and adds -auth-token-file to the arguments.
    [switch]$GenerateAuthToken,
    [switch]$NoStart,
    [int]$KeepReleases = 3
)
. (Join-Path $PSScriptRoot 'common.ps1')

if ($Version -notmatch '^[A-Za-z0-9._+-]+$') { throw "invalid version '$Version'" }
if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) { throw "$Binary does not exist" }

$release = Join-Path $script:Releases $Version
New-Item -ItemType Directory -Force -Path $release, $script:ConfigDir, $script:StateDir | Out-Null
foreach ($d in 'audit', 'jobs', 'records') { New-Item -ItemType Directory -Force -Path (Join-Path $script:StateDir $d) | Out-Null }

if ($Reconfigure -or -not (Test-Path -LiteralPath $script:ArgsFile)) {
    $lines = @(
        "-listen $Listen",
        ('-audit-log "{0}"' -f (Join-Path $script:StateDir 'audit\audit.jsonl')),
        ('-job-dir "{0}"' -f (Join-Path $script:StateDir 'jobs')),
        ('-record-dir "{0}"' -f (Join-Path $script:StateDir 'records'))
    )
    if ($ExtraArgs) { $lines += $ExtraArgs }
    Set-Content -LiteralPath $script:ArgsFile -Value $lines -Encoding UTF8
    Write-Host "install: wrote $($script:ArgsFile)"
} else {
    Write-Host "install: keeping existing $($script:ArgsFile)"
}

if ($GenerateAuthToken) {
    if (-not (Test-Path -LiteralPath $script:TokenFile)) {
        $bytes = New-Object byte[] 24
        [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
        $token = [Convert]::ToBase64String($bytes).Replace('+', '-').Replace('/', '_')
        # No BOM and no newline: the file is the token.
        [IO.File]::WriteAllText($script:TokenFile, $token, (New-Object Text.UTF8Encoding $false))
        Write-Host "install: access token written to $($script:TokenFile)"
        Write-Host "install: the access token is: $token"
        Write-Host 'install: every caller must now send it as "Authorization: Bearer <token>"; the remote desktop page asks for it once'
    } else {
        Write-Host "install: keeping the access token in $($script:TokenFile)"
    }
    $configured = @(Get-Content -LiteralPath $script:ArgsFile)
    if (-not ($configured -match '^-auth-token-file\s')) {
        $configured += ('-auth-token-file "{0}"' -f $script:TokenFile)
        Set-Content -LiteralPath $script:ArgsFile -Value $configured -Encoding UTF8
    }
}

# The running agent holds its executable open; stop it before replacing a
# release in place or switching the junction.
Stop-Agent -TaskName $TaskName
# Windows can keep the executable of a process that has just ended locked
# for a moment; a copy made too early fails with "in use".
$target = Join-Path $release 'vm-agent.exe'
for ($attempt = 1; ; $attempt++) {
    try { Copy-Item -LiteralPath $Binary -Destination $target -Force; break }
    catch { if ($attempt -ge 20) { throw }; Start-Sleep -Milliseconds 500 }
}

$old = Get-JunctionTarget $script:Current
if ($old -and ($old.TrimEnd('\') -ne $release.TrimEnd('\'))) { Set-Junction $script:Previous $old }
Set-Junction $script:Current $release

$keep = @((Get-JunctionTarget $script:Current), (Get-JunctionTarget $script:Previous)) | Where-Object { $_ } | ForEach-Object { $_.TrimEnd('\') }
Get-ChildItem -LiteralPath $script:Releases -Directory | Sort-Object LastWriteTime -Descending | Select-Object -Skip $KeepReleases |
    Where-Object { $keep -notcontains $_.FullName.TrimEnd('\') } | Remove-Item -Recurse -Force

Register-AgentTask -TaskName $TaskName -RunLevel $RunLevel
if (-not $NoStart) {
    Start-ScheduledTask -TaskName $TaskName
    $running = Wait-AgentReady
    Write-Host "install: vm-agent $running is serving"
}
Write-Host "install: current -> $release"
