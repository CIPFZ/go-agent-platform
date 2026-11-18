<#
.SYNOPSIS
  Install, remove or inspect the service that lets the web remote desktop
  show and operate the lock screen.

.DESCRIPTION
  Run as administrator. A user process cannot see or drive the lock screen,
  so "install" adds a Windows service (vm-agent-desktop, LocalSystem,
  automatic start) that keeps a helper running as SYSTEM in the console
  session. vm-agent talks to that helper only while the desktop is locked
  (docs/remote-desktop-design.md 5.14).

  The service must not run files a normal user can replace, so install
  copies vm-agent.exe and ffmpeg (with the DLLs beside it) to
  %ProgramFiles%\vm-agent and runs them from there. Run install again after
  upgrading vm-agent to refresh that copy.

  install    copies the files, creates and starts the service.
  uninstall  stops and deletes the service and removes the copied files and
             its logs.
  status     reports the service, the helper and the files.

  Logs: %ProgramData%\vm-agent\desktop-service.log and desktop-helper.log.

  While the service is installed, whoever can reach vm-agent's port can see
  the lock screen and type into it. vm-agent has no authentication (spec
  D9): restrict the port at the network layer.

.EXAMPLE
  .\secure-desktop.ps1 -Action install -Binary "$env:LOCALAPPDATA\vm-agent\current\vm-agent.exe" -FFmpeg C:\tools\ffmpeg\bin\ffmpeg.exe
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('install', 'uninstall', 'status')][string]$Action,
    [string]$Binary = '',
    [string]$FFmpeg = ''
)
Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'

$name = 'vm-agent-desktop'
$dir = Join-Path $env:ProgramFiles 'vm-agent'
$exe = Join-Path $dir 'vm-agent.exe'
$ffmpegDir = Join-Path $dir 'ffmpeg'
$logs = @('desktop-service.log', 'desktop-service.log.1', 'desktop-helper.log', 'desktop-helper.log.1') | ForEach-Object { Join-Path (Join-Path $env:ProgramData 'vm-agent') $_ }

function Say([string]$Message) { Write-Output "secure-desktop: $Message" }

function Assert-Admin {
    $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if (-not $admin) { throw "run secure-desktop.ps1 -Action $Action as administrator" }
}

function Get-Helper {
    @(Get-CimInstance Win32_Process -Filter "Name='vm-agent.exe'" | Where-Object { $_.CommandLine -match '-desktop-helper' })
}

function Show-Status {
    $svc = Get-Service -Name $name -ErrorAction SilentlyContinue
    if (-not $svc) { Say 'service: not installed'; return }
    $cim = Get-CimInstance Win32_Service -Filter "Name='$name'"
    Say ("service: {0}, start {1}, account {2}" -f $svc.Status, $cim.StartMode, $cim.StartName)
    Say ("command: {0}" -f $cim.PathName)
    $helpers = @(Get-Helper)
    if ($helpers.Count -eq 0) { Say 'helper: not running' }
    foreach ($h in $helpers) { Say ("helper: pid {0} in session {1}" -f $h.ProcessId, $h.SessionId) }
    foreach ($l in $logs) { if (Test-Path -LiteralPath $l) { Get-Content -LiteralPath $l -Tail 4 | ForEach-Object { Say ("log {0}: {1}" -f (Split-Path $l -Leaf), $_) } } }
}

function Remove-ServiceIfPresent {
    if (Get-Service -Name $name -ErrorAction SilentlyContinue) {
        Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
        & sc.exe delete $name | Out-Null
        $deadline = (Get-Date).AddSeconds(15)
        while ((Get-Service -Name $name -ErrorAction SilentlyContinue) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 300 }
    }
    foreach ($h in @(Get-Helper)) { Stop-Process -Id $h.ProcessId -Force -ErrorAction SilentlyContinue }
}

switch ($Action) {
    'status' { Show-Status }

    'install' {
        Assert-Admin
        if (-not $Binary -or -not (Test-Path -LiteralPath $Binary -PathType Leaf)) { throw '-Binary must name vm-agent.exe' }
        if (-not $FFmpeg -or -not (Test-Path -LiteralPath $FFmpeg -PathType Leaf)) { throw '-FFmpeg must name ffmpeg.exe; the helper needs it to show the lock screen' }
        Remove-ServiceIfPresent
        if (Test-Path -LiteralPath $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
        New-Item -ItemType Directory -Force -Path $ffmpegDir | Out-Null
        Copy-Item -LiteralPath $Binary -Destination $exe -Force
        $src = Split-Path -Parent (Resolve-Path -LiteralPath $FFmpeg)
        Copy-Item -LiteralPath $FFmpeg -Destination (Join-Path $ffmpegDir 'ffmpeg.exe') -Force
        Get-ChildItem -LiteralPath $src -Filter '*.dll' | ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $ffmpegDir -Force }
        Say "copied vm-agent.exe and ffmpeg to $dir"

        $command = '"{0}" -desktop-service -ffmpeg "{1}"' -f $exe, (Join-Path $ffmpegDir 'ffmpeg.exe')
        New-Service -Name $name -BinaryPathName $command -DisplayName 'vm-agent secure desktop' -StartupType Automatic `
            -Description 'Lets the vm-agent web remote desktop show and operate the lock screen (docs/remote-desktop-design.md 5.14).' | Out-Null
        # Restart the service if it dies; the service restarts the helper.
        & sc.exe failure $name reset= 86400 actions= restart/5000/restart/5000/restart/60000 | Out-Null
        Start-Service -Name $name
        $deadline = (Get-Date).AddSeconds(15)
        while (@(Get-Helper).Count -eq 0 -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
        Show-Status
        if (@(Get-Helper).Count -eq 0) { throw 'the service started but its helper did not; see the logs above' }
    }

    'uninstall' {
        Assert-Admin
        Remove-ServiceIfPresent
        if (Test-Path -LiteralPath $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
        foreach ($l in $logs) { if (Test-Path -LiteralPath $l) { Remove-Item -LiteralPath $l -Force } }
        if (Get-Service -Name $name -ErrorAction SilentlyContinue) { throw 'the service is still present (it may be marked for deletion until a restart)' }
        Say 'removed'
    }
}
