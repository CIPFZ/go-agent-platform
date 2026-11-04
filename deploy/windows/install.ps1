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

# The running agent holds its executable open; stop it before replacing a
# release in place or switching the junction.
Stop-Agent -TaskName $TaskName
Copy-Item -LiteralPath $Binary -Destination (Join-Path $release 'vm-agent.exe') -Force

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
