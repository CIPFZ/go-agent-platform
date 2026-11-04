# Shared helpers of the vm-agent Windows deployment scripts. Dot-source it:
#   . (Join-Path $PSScriptRoot 'common.ps1')
#
# Layout under %LOCALAPPDATA%\vm-agent:
#   releases\<version>\vm-agent.exe   one directory per release
#   current, previous                 directory junctions to releases
#   config\vm-agent.args              agent arguments, kept across upgrades
#   state\                            audit log, jobs, recordings, lock and log

Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'

$script:Root = Join-Path $env:LOCALAPPDATA 'vm-agent'
$script:Releases = Join-Path $script:Root 'releases'
$script:Current = Join-Path $script:Root 'current'
$script:Previous = Join-Path $script:Root 'previous'
$script:ConfigDir = Join-Path $script:Root 'config'
$script:ArgsFile = Join-Path $script:ConfigDir 'vm-agent.args'
$script:StateDir = Join-Path $script:Root 'state'
$script:LockFile = Join-Path $script:StateDir 'vm-agent.lock'

function Get-JunctionTarget([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        return [string]($item.Target | Select-Object -First 1)
    }
    return $null
}

# Set-Junction points Path at Target, replacing an existing junction without
# touching the directory it pointed to.
function Set-Junction([string]$Path, [string]$Target) {
    if (Test-Path -LiteralPath $Path) {
        $item = Get-Item -LiteralPath $Path -Force
        if (-not ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "$Path exists and is not a junction"
        }
        # Removes the reparse point only; the target directory stays.
        [IO.Directory]::Delete($Path)
    }
    New-Item -ItemType Junction -Path $Path -Target $Target | Out-Null
}

# Get-AgentProcesses lists vm-agent.exe processes started from this install.
function Get-AgentProcesses {
    $root = $script:Root.TrimEnd('\') + '\'
    @(Get-CimInstance Win32_Process -Filter "Name='vm-agent.exe'" | Where-Object {
            $_.ExecutablePath -and ($_.ExecutablePath.StartsWith($root, [StringComparison]::OrdinalIgnoreCase) -or
                (Resolve-JunctionPath $_.ExecutablePath).StartsWith($root, [StringComparison]::OrdinalIgnoreCase))
        })
}

function Resolve-JunctionPath([string]$Path) {
    try { return (Get-Item -LiteralPath $Path -Force).FullName } catch { return $Path }
}

# Stop-Agent ends the supervisor named in the lock file. Its child sees its
# stdin pipe close and shuts down gracefully (spec 4.4: recordings finished,
# jobs killed); anything still running after the timeout is terminated.
function Stop-Agent([string]$TaskName, [int]$TimeoutSeconds = 30) {
    if (Test-Path -LiteralPath $script:LockFile) {
        $supervisor = [int]((Get-Content -LiteralPath $script:LockFile -Raw).Trim())
        Stop-Process -Id $supervisor -Force -ErrorAction SilentlyContinue
    }
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while (@(Get-AgentProcesses).Count -gt 0 -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 250 }
    foreach ($p in Get-AgentProcesses) { Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue }
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    }
}

function Get-ListenAddress {
    $configured = Get-Content -LiteralPath $script:ArgsFile -Raw
    if ($configured -match '-listen\s+(\S+)') { return $Matches[1] }
    return '0.0.0.0:7070'
}

# Wait-AgentReady polls /v1/probe on the configured port and returns the
# reported version.
function Wait-AgentReady([int]$TimeoutSeconds = 30) {
    $port = (Get-ListenAddress).Split(':')[-1]
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $probe = Invoke-RestMethod -Uri "http://127.0.0.1:$port/v1/probe" -TimeoutSec 2
            if ($probe.ok) { return $probe.result.version }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    throw "vm-agent did not answer on port $port within $TimeoutSeconds s; see $(Join-Path $script:StateDir 'vm-agent.log')"
}

# Register-AgentTask (re)creates the scheduled task of spec 4.1: logon trigger
# for the current user, a daily last-resort trigger (a second instance exits
# when the lock file names a live supervisor), no task-level restart and no
# execution time limit.
function Register-AgentTask([string]$TaskName, [string]$RunLevel) {
    $user = "$env:USERDOMAIN\$env:USERNAME"
    $agentArgs = (Get-Content -LiteralPath $script:ArgsFile -Raw).Trim() -replace '\s+', ' '
    $action = New-ScheduledTaskAction -Execute (Join-Path $script:Current 'vm-agent.exe') `
        -Argument ("-supervise " + $agentArgs) -WorkingDirectory $script:Root
    $logon = New-ScheduledTaskTrigger -AtLogOn -User $user
    $daily = New-ScheduledTaskTrigger -Daily -At '03:17'
    $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel $RunLevel
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -StartWhenAvailable
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger @($logon, $daily) -Principal $principal `
        -Settings $settings -Description 'vm-agent desktop automation service (docs/spec.md 4.1)' -Force | Out-Null
}
