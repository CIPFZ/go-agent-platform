<#
.SYNOPSIS
  Rehearse install, upgrade and rollback (deploy/windows) in a scratch
  %LOCALAPPDATA% under Windows PowerShell 5.1, without starting the agent.

.DESCRIPTION
  Checks the release junctions after each step and that config\vm-agent.args
  and a file under state\records survive the upgrade and the rollback (spec
  13.2 acceptance 4). Registers and finally removes the scheduled task named
  -TaskName, so it does not touch a real installation.
#>
param([Parameter(Mandatory)][string]$Binary, [string]$TaskName = 'vm-agent-ci')
Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'
$scripts = Join-Path $PSScriptRoot '..\windows'
$env:LOCALAPPDATA = Join-Path ([IO.Path]::GetTempPath()) ('vm-agent-install-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $env:LOCALAPPDATA | Out-Null
$root = Join-Path $env:LOCALAPPDATA 'vm-agent'

function Target([string]$Name) { [string]((Get-Item -LiteralPath (Join-Path $root $Name) -Force).Target | Select-Object -First 1) }
function Expect([string]$What, $Got, $Want) {
    if ($Got -ne $Want) { throw "${What}: got '$Got', want '$Want'" }
    Write-Host "ok: $What = $Got"
}

try {
    & (Join-Path $scripts 'install.ps1') -Binary $Binary -Version t1 -Listen 127.0.0.1:17079 -TaskName $TaskName -NoStart
    Expect 'current after install' (Target current) (Join-Path $root 'releases\t1')
    $argsHash = (Get-FileHash (Join-Path $root 'config\vm-agent.args')).Hash
    $marker = Join-Path $root 'state\records\marker.mp4'
    Set-Content -LiteralPath $marker -Value 'recording'

    & (Join-Path $scripts 'install.ps1') -Binary $Binary -Version t2 -Listen 0.0.0.0:1 -TaskName $TaskName -NoStart
    Expect 'current after upgrade' (Target current) (Join-Path $root 'releases\t2')
    Expect 'previous after upgrade' (Target previous) (Join-Path $root 'releases\t1')
    Expect 'args kept by upgrade' (Get-FileHash (Join-Path $root 'config\vm-agent.args')).Hash $argsHash

    & (Join-Path $scripts 'rollback.ps1') -TaskName $TaskName -NoStart
    Expect 'current after rollback' (Target current) (Join-Path $root 'releases\t1')
    Expect 'previous after rollback' (Target previous) (Join-Path $root 'releases\t2')
    Expect 'args kept by rollback' (Get-FileHash (Join-Path $root 'config\vm-agent.args')).Hash $argsHash
    Expect 'recording kept' (Get-Content -LiteralPath $marker) 'recording'
    $task = Get-ScheduledTask -TaskName $TaskName
    Expect 'task runs -supervise' ($task.Actions[0].Arguments -like '-supervise -listen 127.0.0.1:17079 *') $true
} finally {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $env:LOCALAPPDATA -Recurse -Force -ErrorAction SilentlyContinue
}
