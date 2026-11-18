<#
.SYNOPSIS
  Install, remove or inspect the virtual display driver used by the web
  remote desktop while no display is active (a closed laptop lid).

.DESCRIPTION
  Run as administrator. The driver is Amyuni's "USB Mobile Monitor Virtual
  Display" (usbmmidd v2), an indirect display driver whose catalog is signed
  by the Microsoft Windows Hardware Compatibility Publisher. It is not part
  of this repository: -Action install downloads the vendor's package,
  verifies its SHA-256 and installs it with the vendor's deviceinstaller.
  The package and its License.txt are kept under
  %ProgramData%\vm-agent\virtual-display. The licence asks for an
  acknowledgement: this product uses the USB Mobile Monitor Virtual Display
  driver, copyright Amyuni Technologies Inc. (docs/remote-desktop-design.md
  5.11).

  install    downloads, verifies and installs the driver. No monitor is added:
             vm-agent adds one while a session needs it. -Width and -Height
             add a resolution to the driver's list and make it the default
             (the driver offers nine presets up to 3840x2160).
  uninstall  removes every virtual monitor, the device, the driver package
             and the downloaded files.
  status     reports whether the driver is installed and its monitors.
  plug       adds one virtual monitor (for testing).
  unplug     removes one virtual monitor (for testing).

  What install leaves on the system: one device under Display adapters, one
  driver package in the driver store, and the driver's parameters under
  HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\WUDF\Services\usbmmIdd.
  No service and nothing that runs at startup.

.EXAMPLE
  .\virtual-display.ps1 -Action install -Width 2560 -Height 1600
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('install', 'uninstall', 'status', 'plug', 'unplug')][string]$Action,
    [int]$Width = 0,
    [int]$Height = 0,
    [string]$PackageUrl = 'https://www.amyuni.com/downloads/usbmmidd_v2.zip',
    [string]$PackageSha256 = '629b51e9944762bae73948171c65d09a79595cf4c771a82ebc003fbba5b24f51',
    # A package already on disk, instead of downloading it.
    [string]$PackageFile = ''
)
Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'

$deviceName = 'USB Mobile Monitor Virtual Display'
$hardwareId = 'usbmmidd'
$root = Join-Path $env:ProgramData 'vm-agent\virtual-display'
$package = Join-Path $root 'usbmmidd_v2'
$installer = Join-Path $package 'deviceinstaller64.exe'
$serviceKey = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\WUDF\Services\usbmmIdd'
$monitorsKey = Join-Path $serviceKey 'Parameters\Monitors'

function Say([string]$Message) { Write-Output "virtual-display: $Message" }

function Get-Adapter {
    @(Get-PnpDevice -Class Display -ErrorAction SilentlyContinue | Where-Object { $_.FriendlyName -eq $deviceName })
}

function Get-VirtualMonitors {
    $adapters = @(Get-Adapter)
    if ($adapters.Count -eq 0) { return @() }
    # Monitors created by the driver are children of its adapter.
    @(Get-PnpDevice -Class Monitor -PresentOnly -ErrorAction SilentlyContinue | Where-Object {
            $parent = (Get-PnpDeviceProperty -InstanceId $_.InstanceId -KeyName 'DEVPKEY_Device_Parent' -ErrorAction SilentlyContinue).Data
            $adapters.InstanceId -contains $parent
        })
}

function Invoke-Installer([string[]]$Arguments) {
    if (-not (Test-Path -LiteralPath $installer)) { throw "$installer is missing; run -Action install first" }
    $p = Start-Process -FilePath $installer -ArgumentList $Arguments -WorkingDirectory $package -Wait -PassThru -WindowStyle Hidden
    Say ("deviceinstaller64 {0} -> exit {1}" -f ($Arguments -join ' '), $p.ExitCode)
    return $p.ExitCode
}

function Assert-Admin {
    $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if (-not $admin) { throw "run virtual-display.ps1 -Action $Action as administrator" }
}

function Show-Status {
    $adapters = @(Get-Adapter)
    if ($adapters.Count -eq 0) { Say 'driver: not installed'; return }
    foreach ($a in $adapters) { Say ("driver: installed, device status {0} ({1})" -f $a.Status, $a.InstanceId) }
    $driver = Get-CimInstance Win32_PnPSignedDriver -Filter "DeviceName='$deviceName'" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($driver) { Say ("driver package: {0}, version {1}, signed by {2}" -f $driver.InfName, $driver.DriverVersion, $driver.Signer) }
    $monitors = @(Get-VirtualMonitors)
    Say ("virtual monitors: {0}" -f $monitors.Count)
    if (Test-Path $monitorsKey) {
        $p = Get-ItemProperty $monitorsKey
        $modes = @(0..9 | ForEach-Object { $p.PSObject.Properties["$_"] } | Where-Object { $_ } | ForEach-Object { $_.Value })
        Say ("resolutions: {0}; default {1}" -f ($modes -join ' '), $p.'(default)')
    }
}

switch ($Action) {
    'status' { Show-Status }

    'install' {
        Assert-Admin
        if (-not [Environment]::Is64BitOperatingSystem) { throw 'only 64-bit Windows is supported' }
        New-Item -ItemType Directory -Force -Path $root | Out-Null
        $zip = Join-Path $root 'usbmmidd_v2.zip'
        if ($PackageFile) {
            Copy-Item -LiteralPath $PackageFile -Destination $zip -Force
        } else {
            Say "downloading $PackageUrl"
            [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
            Invoke-WebRequest -Uri $PackageUrl -OutFile $zip -UseBasicParsing
        }
        $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($hash -ne $PackageSha256.ToLowerInvariant()) {
            Remove-Item -LiteralPath $zip -Force
            throw "the package's SHA-256 is $hash, expected $PackageSha256; nothing was installed"
        }
        Say "package verified ($hash)"
        if (Test-Path -LiteralPath $package) { Remove-Item -LiteralPath $package -Recurse -Force }
        Expand-Archive -LiteralPath $zip -DestinationPath $root -Force
        $signer = (Get-AuthenticodeSignature -LiteralPath (Join-Path $package 'usbmmidd.cat')).SignerCertificate.Subject
        Say "catalog signed by: $signer"
        if ($signer -notmatch 'Microsoft Windows Hardware Compatibility Publisher') { throw 'the driver catalog is not signed by Microsoft; nothing was installed' }

        if (@(Get-Adapter).Count -eq 0) {
            $code = Invoke-Installer @('install', 'usbmmidd.inf', $hardwareId)
            # The device appears a moment after the installer returns.
            $deadline = (Get-Date).AddSeconds(30)
            while (@(Get-Adapter).Count -eq 0 -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
            if (@(Get-Adapter).Count -eq 0) { throw "the driver did not install (deviceinstaller64 exit $code)" }
        } else {
            Say 'the driver is already installed'
        }

        if ($Width -gt 0 -and $Height -gt 0) {
            $deadline = (Get-Date).AddSeconds(15)
            while (-not (Test-Path $monitorsKey) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
            if (-not (Test-Path $monitorsKey)) { throw "$monitorsKey does not exist; the resolution was not set" }
            $mode = "$Width,$Height"
            $p = Get-ItemProperty $monitorsKey
            $have = @(0..9 | ForEach-Object { $p.PSObject.Properties["$_"] } | Where-Object { $_ -and $_.Value -eq $mode })
            if ($have.Count -eq 0) {
                # Slot 9 is free in the driver's own list of nine presets.
                New-ItemProperty -Path $monitorsKey -Name '9' -Value $mode -PropertyType String -Force | Out-Null
            }
            Set-ItemProperty -Path $monitorsKey -Name '(default)' -Value $mode
            Say "default resolution set to $mode"
        }
        Show-Status
    }

    'uninstall' {
        Assert-Admin
        if (@(Get-Adapter).Count -eq 0 -and -not (Test-Path -LiteralPath $root) -and -not (Test-Path $serviceKey)) { Say 'nothing to remove'; return }
        if ((Test-Path -LiteralPath $installer) -and @(Get-Adapter).Count -gt 0) {
            for ($i = 0; $i -lt 4 -and @(Get-VirtualMonitors).Count -gt 0; $i++) {
                [void](Invoke-Installer @('enableidd', '0'))
                Start-Sleep -Seconds 1
            }
            [void](Invoke-Installer @('stop', $hardwareId))
            [void](Invoke-Installer @('remove', $hardwareId))
        }
        # Whatever the vendor tool left: the device, then the driver package.
        foreach ($a in @(Get-Adapter)) { & pnputil.exe /remove-device $a.InstanceId | ForEach-Object { Say "pnputil: $_" } }
        $published = @(Get-WindowsDriver -Online -ErrorAction SilentlyContinue | Where-Object { $_.OriginalFileName -match '\\usbmmidd\.inf' })
        foreach ($d in $published) { & pnputil.exe /delete-driver $d.Driver /uninstall | ForEach-Object { Say "pnputil: $_" } }
        if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
        # The driver's parameters (its resolution list) outlive the driver.
        if (Test-Path $serviceKey) { Remove-Item -Path $serviceKey -Recurse -Force }
        if (@(Get-Adapter).Count -gt 0) { throw 'the device is still present' }
        Say 'removed'
    }

    'plug' {
        Assert-Admin
        [void](Invoke-Installer @('enableidd', '1'))
        Start-Sleep -Seconds 2
        Show-Status
    }

    'unplug' {
        Assert-Admin
        [void](Invoke-Installer @('enableidd', '0'))
        Start-Sleep -Seconds 2
        Show-Status
    }
}
