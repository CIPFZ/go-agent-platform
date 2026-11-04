<#
.SYNOPSIS
  Configure the Windows desktop-session preconditions of spec 4.2.

.DESCRIPTION
  Run as administrator. Applies, for the machine and the user given by -User
  (default: the current user):
    - no lock screen, screensaver or idle lock; no sign-in on wake;
    - display, sleep and hibernate timeouts off, hibernation disabled;
    - no secure-desktop UAC prompts: ConsentPromptBehaviorAdmin=0 and
      PromptOnSecureDesktop=0 (EnableLUA stays on);
    - optionally no RDP logons (-DisableRdp), since an RDP session is not the
      console (use console-session.ps1 to return a session to the console);
    - optionally automatic logon (-AutoLogonUser with -AutoLogonPassword).
      Winlogon keeps that password readable by administrators in the
      registry; use it only on dedicated automation VMs.
  vm-agent's probe reports the outcome (session_locked, uac_prompt,
  session_type). Some settings take effect at the next logon.
#>
[CmdletBinding()]
param(
    [switch]$DisableRdp,
    [string]$AutoLogonUser = '',
    [SecureString]$AutoLogonPassword,
    [string]$AutoLogonDomain = $env:COMPUTERNAME
)
Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { throw 'run preconditions.ps1 as administrator' }

function Set-Reg([string]$Path, [string]$Name, $Value, [string]$Type = 'DWord') {
    if (-not (Test-Path $Path)) { New-Item -Path $Path -Force | Out-Null }
    New-ItemProperty -Path $Path -Name $Name -Value $Value -PropertyType $Type -Force | Out-Null
    Write-Host ("preconditions: {0}\{1} = {2}" -f $Path, $Name, $Value)
}

# Not locking (spec 4.2 row 1).
Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\Personalization' 'NoLockScreen' 1
Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System' 'InactivityTimeoutSecs' 0
Set-Reg 'HKCU:\Software\Policies\Microsoft\Windows\Control Panel\Desktop' 'ScreenSaveActive' '0' 'String'
Set-Reg 'HKCU:\Software\Policies\Microsoft\Windows\Control Panel\Desktop' 'ScreenSaverIsSecure' '0' 'String'
Set-Reg 'HKCU:\Control Panel\Desktop' 'ScreenSaveActive' '0' 'String'
Set-Reg 'HKCU:\Software\Microsoft\Windows NT\CurrentVersion\Winlogon' 'EnableGoodbye' 0
powercfg /setacvalueindex SCHEME_CURRENT SUB_NONE CONSOLELOCK 0
powercfg /setdcvalueindex SCHEME_CURRENT SUB_NONE CONSOLELOCK 0
foreach ($setting in 'monitor-timeout-ac', 'monitor-timeout-dc', 'standby-timeout-ac', 'standby-timeout-dc', 'hibernate-timeout-ac', 'hibernate-timeout-dc') {
    powercfg /change $setting 0
}
# Not sleeping (row 2).
powercfg /h off
powercfg /setactive SCHEME_CURRENT
Write-Host 'preconditions: power timeouts off, hibernation disabled'

# No secure desktop (row 3).
Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System' 'ConsentPromptBehaviorAdmin' 0
Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System' 'PromptOnSecureDesktop' 0

# Console session (row 4).
if ($DisableRdp) {
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\Terminal Server' 'fDenyTSConnections' 1
}

# Graphical session present (row 5): automatic logon.
if ($AutoLogonUser) {
    if (-not $AutoLogonPassword) { throw '-AutoLogonUser needs -AutoLogonPassword' }
    $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($AutoLogonPassword))
    $winlogon = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon'
    Set-Reg $winlogon 'AutoAdminLogon' '1' 'String'
    Set-Reg $winlogon 'DefaultUserName' $AutoLogonUser 'String'
    Set-Reg $winlogon 'DefaultDomainName' $AutoLogonDomain 'String'
    New-ItemProperty -Path $winlogon -Name 'DefaultPassword' -Value $plain -PropertyType String -Force | Out-Null
    Write-Host "preconditions: automatic logon as $AutoLogonDomain\$AutoLogonUser (password stored by Winlogon)"
}
Write-Host 'preconditions: done; sign out and back in (or reboot) for every setting to apply'
