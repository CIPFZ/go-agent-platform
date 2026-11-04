<#
.SYNOPSIS
  Move the current RDP session back to the console (spec 4.2).

.DESCRIPTION
  Run inside the RDP session, as administrator, before disconnecting. The
  session stays logged in on the physical console, so vm-agent keeps a
  console session_type and a usable desktop after the RDP client goes away.
  The RDP connection drops immediately.
#>
Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'
$session = (Get-Process -Id $PID).SessionId
Write-Host "console-session: moving session $session to the console"
& "$env:SystemRoot\System32\tscon.exe" $session /dest:console
if ($LASTEXITCODE -ne 0) { throw "tscon failed with exit code $LASTEXITCODE" }
