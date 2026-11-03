<#
.SYNOPSIS
  Windows end-to-end regression and DPI matrix for vm-agent (spec 12, 13.1).

.DESCRIPTION
  Run in the interactive console session of a Windows 10/11 VM (not over
  RDP, which changes the session type and can lock the console). The script
  starts its own vm-agent on a test port unless -AgentUrl is given, then:

    1. probe/capabilities: Per-Monitor V2 awareness, display ready, session
       type and UAC state;
    2. capture latency: 200 JPEG captures of the primary display, p50/p99 of
       elapsed_ms (acceptance 2 when the primary display is 1920x1080);
    3. click accuracy (acceptance 1): winapp.exe covers the primary display
       and logs the screen position of every WM_LBUTTONDOWN; a 6x4 grid of
       clicks is compared point by point, the maximum error must be <= 2 px;
    4. a repeated X-Request-Id clicks once (acceptance 4) and a 50-action
       batch arrives in order;
    5. Notepad regression: launch on a file, activate, type, ctrl+s, read the
       file back, capture, close;
    6. restart (acceptance 6, own agent only): a program started with
       process/launch survives killing vm-agent, a running exec does not;
    7. -IncludeLock: locks the workstation, expects session_locked and
       no_display, and waits for you to unlock.

  DPI matrix: set the primary display to 100 %, sign out and back in, run the
  script; repeat at 125 % and 150 %. Each run appends one row per check to
  -Report, labelled with the scale vm-agent reports.

  Build the binaries on any machine with Go:
    GOOS=windows GOARCH=amd64 go build -o vm-agent.exe ./cmd/vm-agent
    GOOS=windows GOARCH=amd64 go build -o winapp.exe   ./test/winapp

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File windows-e2e.ps1 -AgentExe .\vm-agent.exe -WinAppExe .\winapp.exe
#>
[CmdletBinding()]
param(
    [string]$AgentExe = '.\vm-agent.exe',
    [string]$WinAppExe = '.\winapp.exe',
    [string]$AgentUrl = '',
    [int]$Port = 17070,
    [string]$Report = '.\windows-e2e-report.csv',
    [int]$CaptureSamples = 200,
    [switch]$SkipRestart,
    [switch]$IncludeLock
)

Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http

$script:Http = New-Object System.Net.Http.HttpClient
$script:Http.Timeout = [TimeSpan]::FromMinutes(15)
$script:Work = Join-Path $env:TEMP ("vm-agent-e2e-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $script:Work | Out-Null
$script:Results = New-Object System.Collections.ArrayList
$script:Scale = 'unknown'
$script:AgentProc = $null
if ($AgentUrl -eq '') { $script:Base = "http://127.0.0.1:$Port" } else { $script:Base = $AgentUrl.TrimEnd('/') }

function New-RequestId { 'win-e2e-' + [Guid]::NewGuid().ToString('N') }

# Invoke-Agent returns @{ Status; Body (parsed envelope); Headers }.
function Invoke-Agent {
    param([string]$Method, [string]$Path, $Body = $null, [string]$RequestId = '', [int]$TimeoutMs = 0)
    $req = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::new($Method)), ($script:Base + $Path)
    if ($RequestId -eq '') { $RequestId = New-RequestId }
    $req.Headers.Add('X-Request-Id', $RequestId)
    if ($TimeoutMs -gt 0) { $req.Headers.Add('X-Timeout-Ms', [string]$TimeoutMs) }
    if ($null -ne $Body) {
        $json = ConvertTo-Json -InputObject $Body -Depth 10 -Compress
        $req.Content = New-Object System.Net.Http.StringContent $json, ([System.Text.Encoding]::UTF8), 'application/json'
    }
    $resp = $script:Http.SendAsync($req).GetAwaiter().GetResult()
    $bytes = $resp.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
    $text = [System.Text.Encoding]::UTF8.GetString($bytes)
    $parsed = $null
    if ($text.Length -gt 0 -and $text[0] -eq '{') { $parsed = ConvertFrom-Json $text }
    return @{ Status = [int]$resp.StatusCode; Body = $parsed; Headers = $resp.Headers; Raw = $bytes }
}

# Call fails the script on ok=false and returns the result object.
function Call {
    param([string]$Method, [string]$Path, $Body = $null, [string]$RequestId = '', [int]$TimeoutMs = 0)
    $r = Invoke-Agent -Method $Method -Path $Path -Body $Body -RequestId $RequestId -TimeoutMs $TimeoutMs
    if ($null -eq $r.Body -or -not $r.Body.ok) {
        $err = ''
        if ($null -ne $r.Body) { $err = ConvertTo-Json $r.Body.error -Compress -Depth 5 }
        throw "$Method $Path failed: HTTP $($r.Status) $err"
    }
    return $r.Body.result
}

function Add-Result {
    param([string]$Check, [bool]$Pass, [string]$Detail)
    $state = 'FAIL'
    if ($Pass) { $state = 'PASS' }
    Write-Host ("[{0}] {1}: {2}" -f $state, $Check, $Detail)
    [void]$script:Results.Add([pscustomobject]@{
            time = (Get-Date).ToString('s'); host = $env:COMPUTERNAME; scale = $script:Scale
            check = $Check; result = $state; detail = $Detail
        })
}

function Start-Agent {
    $agentArgs = @('-listen', "127.0.0.1:$Port",
        '-audit-log', (Join-Path $script:Work 'audit.jsonl'),
        '-job-dir', (Join-Path $script:Work 'jobs'),
        '-record-dir', (Join-Path $script:Work 'records'))
    $script:AgentProc = Start-Process -FilePath $AgentExe -ArgumentList $agentArgs -PassThru -WindowStyle Hidden `
        -RedirectStandardError (Join-Path $script:Work 'agent.err') -RedirectStandardOutput (Join-Path $script:Work 'agent.out')
    for ($i = 0; $i -lt 100; $i++) {
        try { $null = Call GET '/v1/probe'; return } catch { Start-Sleep -Milliseconds 100 }
    }
    throw 'vm-agent did not become ready'
}

function Stop-Agent {
    if ($null -ne $script:AgentProc -and -not $script:AgentProc.HasExited) {
        Stop-Process -Id $script:AgentProc.Id -Force
        $script:AgentProc.WaitForExit()
    }
}

function Wait-Window {
    param([string]$TitlePart, [int]$Seconds = 20)
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        $list = Call GET '/v1/window/list'
        foreach ($w in $list.windows) { if ($w.title -like "*$TitlePart*") { return $w } }
        Start-Sleep -Milliseconds 250
    }
    throw "no window titled *$TitlePart*"
}

function Read-Clicks {
    param([string]$Log, [int]$Count, [int]$Seconds = 10)
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $lines = @()
        if (Test-Path $Log) { $lines = @(Get-Content $Log | Where-Object { $_ -match '^-?\d+,-?\d+$' }) }
        if ($lines.Count -ge $Count) { break }
        Start-Sleep -Milliseconds 100
    } while ((Get-Date) -lt $deadline)
    return , $lines
}

function Percentile {
    param([double[]]$Values, [double]$P)
    $sorted = $Values | Sort-Object
    $idx = [int][Math]::Floor(($sorted.Count - 1) * $P)
    return $sorted[$idx]
}

try {
    if ($AgentUrl -eq '') { Start-Agent }

    # 1. probe and capabilities
    $probe = Call GET '/v1/probe'
    if ($null -ne $probe.display.primary) {
        $script:Scale = [string]$probe.display.primary.scale
        $res = "{0}x{1}" -f $probe.display.primary.w, $probe.display.primary.h
    } else { $res = 'none' }
    Add-Result 'probe.platform' ($probe.platform -eq 'windows') $probe.platform
    Add-Result 'probe.dpi_awareness' ($probe.display.dpi_awareness -eq 'per_monitor_v2') $probe.display.dpi_awareness
    Add-Result 'probe.display_ready' ([bool]$probe.display.ready) "primary $res scale $($script:Scale)"
    Add-Result 'probe.session' ($probe.session_type -eq 'console' -and -not $probe.session_locked) "type=$($probe.session_type) locked=$($probe.session_locked) uac_prompt=$($probe.uac_prompt)"
    $caps = Call GET '/v1/capabilities'
    Add-Result 'capabilities.actions' (($caps.actions -join ',') -like 'screen,input,window,exec,process,file*') ($caps.actions -join ',')

    # 2. capture latency
    $elapsed = New-Object System.Collections.Generic.List[double]
    for ($i = 0; $i -lt $CaptureSamples; $i++) {
        $r = Invoke-Agent POST '/v1/screen/capture' @{ format = 'jpeg' }
        $elapsed.Add([double]$r.Body.elapsed_ms)
    }
    $p50 = Percentile $elapsed.ToArray() 0.5
    $p99 = Percentile $elapsed.ToArray() 0.99
    $gate = ($res -ne '1920x1080') -or ($p50 -lt 60 -and $p99 -lt 150)
    Add-Result 'capture.latency_jpeg' $gate ("{0} samples at {1}: p50 {2} ms, p99 {3} ms (gate p50<60 p99<150 applies at 1920x1080)" -f $CaptureSamples, $res, $p50, $p99)

    # 3. click accuracy on a self-drawn full-screen window
    $clickLog = Join-Path $script:Work 'clicks.log'
    $app = Start-Process -FilePath $WinAppExe -ArgumentList @('-log', $clickLog, '-title', 'vm-agent-dpi-test') -PassThru
    $win = Wait-Window 'vm-agent-dpi-test'
    $null = Call POST '/v1/window/activate' @{ id = $win.id }
    $bounds = (Call GET '/v1/screen/info').displays[0].bounds
    $points = @()
    foreach ($fx in 0.03, 0.2, 0.4, 0.6, 0.8, 0.97) {
        foreach ($fy in 0.04, 0.35, 0.65, 0.96) {
            $points += , @([int]($bounds.x + $fx * $bounds.w), [int]($bounds.y + $fy * $bounds.h))
        }
    }
    foreach ($p in $points) { $null = Call POST '/v1/input/mouse/click' @{ x = $p[0]; y = $p[1] } }
    $lines = Read-Clicks $clickLog $points.Count
    $worst = 0; $bad = 0
    for ($i = 0; $i -lt [Math]::Min($lines.Count, $points.Count); $i++) {
        $xy = $lines[$i].Split(',')
        $d = [Math]::Max([Math]::Abs([int]$xy[0] - $points[$i][0]), [Math]::Abs([int]$xy[1] - $points[$i][1]))
        if ($d -gt $worst) { $worst = $d }
        if ($d -gt 2) { $bad++ }
    }
    Add-Result 'click.accuracy' ($lines.Count -eq $points.Count -and $worst -le 2) ("{0}/{1} clicks logged, max error {2} px, {3} beyond 2 px, scale {4}" -f $lines.Count, $points.Count, $worst, $bad, $script:Scale)

    # 4. dedup and batch order on the same window
    $before = (Read-Clicks $clickLog 0 0).Count
    $rid = New-RequestId
    $first = Invoke-Agent POST '/v1/input/mouse/click' @{ x = $points[0][0]; y = $points[0][1] } $rid
    $second = Invoke-Agent POST '/v1/input/mouse/click' @{ x = $points[0][0]; y = $points[0][1] } $rid
    Start-Sleep -Milliseconds 500
    $after = (Read-Clicks $clickLog 0 0).Count
    $hit = $false
    if ($second.Headers.Contains('X-Dedup-Hit')) { $hit = $true }
    Add-Result 'dedup.click_once' ($first.Body.ok -and $hit -and ($after - $before) -eq 1) ("clicks added {0}, replay hit {1}" -f ($after - $before), $hit)
    $actions = @()
    for ($i = 0; $i -lt 50; $i++) { $actions += @{ type = 'mouse.click'; x = $bounds.x + 20 + $i * 10; y = $bounds.y + 300 } }
    $start = (Read-Clicks $clickLog 0 0).Count
    $batch = Call POST '/v1/input/batch' @{ actions = $actions }
    $lines = Read-Clicks $clickLog ($start + 50)
    $ordered = $true
    for ($i = 0; $i -lt 50; $i++) {
        if ($lines.Count -le $start + $i -or [int]($lines[$start + $i].Split(',')[0]) -ne $bounds.x + 20 + $i * 10) { $ordered = $false }
    }
    Add-Result 'batch.order' ($batch.executed -eq 50 -and $ordered) ("executed {0}, in order {1}" -f $batch.executed, $ordered)
    $null = Call POST '/v1/window/close' @{ id = $win.id }
    if (-not $app.WaitForExit(5000)) { Stop-Process -Id $app.Id -Force }

    # 5. Notepad regression
    $docs = (Call GET '/v1/file/known_dirs').documents
    $name = 'vm-agent-e2e-' + (Get-Date -Format 'yyyyMMddHHmmss') + '.txt'
    $doc = Join-Path $docs $name
    $null = Call POST '/v1/file/write' @{ path = $doc; content = ''; overwrite = $true }
    $launch = Call POST '/v1/process/launch' @{ argv = @('notepad.exe', $doc) }
    $np = Wait-Window $name
    $active = Call POST '/v1/window/activate' @{ id = $np.id }
    $text = 'vm-agent e2e ' + (Get-Date).ToString('s') + ' ünïcødé 世界'
    $null = Call POST '/v1/input/key/type' @{ text = $text }
    $null = Call POST '/v1/input/key/hotkey' @{ keys = @('ctrl', 's') }
    $content = ''
    for ($i = 0; $i -lt 40; $i++) {
        $read = Call POST '/v1/file/read' @{ path = $doc }
        $content = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($read.content))
        if ($content.Contains($text)) { break }
        Start-Sleep -Milliseconds 250
    }
    Add-Result 'notepad.type_save_read' ($content.Contains($text)) ("file has {0} chars" -f $content.Length)
    $shot = Call POST '/v1/screen/capture' @{ cursor = $true }
    Add-Result 'notepad.capture' ($shot.width -gt 0 -and $shot.image.Length -gt 1000) ("{0}x{1} sha256 {2}" -f $shot.width, $shot.height, $shot.sha256)
    $null = Call POST '/v1/window/close' @{ id = $np.id }
    $gone = $false
    for ($i = 0; $i -lt 40 -and -not $gone; $i++) {
        $gone = -not (@((Call GET '/v1/window/list').windows | Where-Object { $_.id -eq $np.id }).Count -gt 0)
        Start-Sleep -Milliseconds 250
    }
    Add-Result 'notepad.close' $gone "window $($np.id) closed"
    Remove-Item $doc -ErrorAction SilentlyContinue

    # 6. restart: launch survives, exec does not
    if ($AgentUrl -eq '' -and -not $SkipRestart) {
        $keep = Call POST '/v1/process/launch' @{ argv = @('notepad.exe') }
        $agentPid = $script:AgentProc.Id
        $execReq = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::Post), ($script:Base + '/v1/exec')
        $execReq.Content = New-Object System.Net.Http.StringContent '{"argv":["ping","-n","300","127.0.0.1"]}', ([System.Text.Encoding]::UTF8), 'application/json'
        $pending = $script:Http.SendAsync($execReq)
        $pings = @()
        for ($i = 0; $i -lt 50 -and $pings.Count -eq 0; $i++) {
            $pings = @(Get-CimInstance Win32_Process -Filter "Name='PING.EXE' AND ParentProcessId=$agentPid" | ForEach-Object { $_.ProcessId })
            Start-Sleep -Milliseconds 100
        }
        Stop-Agent
        Start-Sleep -Milliseconds 500
        $alivePing = @($pings | Where-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue })
        $keepAlive = $null -ne (Get-Process -Id $keep.pid -ErrorAction SilentlyContinue)
        Start-Agent
        Add-Result 'restart.launch_survives' $keepAlive "notepad pid $($keep.pid)"
        Add-Result 'restart.exec_cleaned' ($pings.Count -gt 0 -and $alivePing.Count -eq 0) ("exec ping pids {0}, still alive {1}" -f ($pings -join ' '), $alivePing.Count)
        Stop-Process -Id $keep.pid -Force -ErrorAction SilentlyContinue
        $null = $pending
    }

    # 7. lock
    if ($IncludeLock) {
        Write-Host 'Locking the workstation; unlock it within 5 minutes to finish.'
        rundll32.exe 'user32.dll,LockWorkStation'
        Start-Sleep -Seconds 2
        $locked = Call GET '/v1/probe'
        $click = Invoke-Agent POST '/v1/input/mouse/click' @{}
        Add-Result 'lock.probe' ([bool]$locked.session_locked -and -not $locked.display.ready) "session_locked=$($locked.session_locked) ready=$($locked.display.ready)"
        Add-Result 'lock.input_refused' ($click.Status -eq 503 -and $click.Body.error.code -eq 'no_display') "HTTP $($click.Status) $($click.Body.error.code)"
        $deadline = (Get-Date).AddMinutes(5)
        while ((Get-Date) -lt $deadline -and (Call GET '/v1/probe').session_locked) { Start-Sleep -Seconds 2 }
    }
}
catch {
    Add-Result 'script' $false $_.Exception.Message
}
finally {
    if ($AgentUrl -eq '') { Stop-Agent }
    $script:Results | Export-Csv -Path $Report -NoTypeInformation -Append -Encoding UTF8
    $failed = @($script:Results | Where-Object { $_.result -eq 'FAIL' }).Count
    Write-Host ("{0} checks, {1} failed; report appended to {2}" -f $script:Results.Count, $failed, $Report)
    if ($failed -gt 0) { exit 1 }
}
