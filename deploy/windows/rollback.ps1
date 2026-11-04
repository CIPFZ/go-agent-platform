<#
.SYNOPSIS
  Switch vm-agent back to the previous release.

.DESCRIPTION
  Stops the agent, swaps the "current" and "previous" junctions and starts
  the scheduled task again. config\vm-agent.args and state\ (audit log,
  jobs, recordings) are not touched. Running it twice returns to the newer
  release.
#>
[CmdletBinding()]
param([string]$TaskName = 'vm-agent', [switch]$NoStart)
. (Join-Path $PSScriptRoot 'common.ps1')

$cur = Get-JunctionTarget $script:Current
$prev = Get-JunctionTarget $script:Previous
if (-not $prev) { throw "no previous release under $($script:Root)" }
if (-not (Test-Path -LiteralPath (Join-Path $prev 'vm-agent.exe'))) { throw "$prev\vm-agent.exe is missing" }

Stop-Agent -TaskName $TaskName
Set-Junction $script:Current $prev
Set-Junction $script:Previous $cur
if (-not $NoStart) {
    Start-ScheduledTask -TaskName $TaskName
    $running = Wait-AgentReady
    Write-Host "rollback: vm-agent $running is serving"
}
Write-Host "rollback: current -> $prev (previous -> $cur)"
