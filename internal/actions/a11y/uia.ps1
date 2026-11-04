# Collects the UI Automation tree of one window for vm-agent's a11y/tree.
# Input: environment variable VM_AGENT_A11Y_HELPER holding the JSON request
# ({target:{id,title,pid}, max_depth, max_nodes, origin_x, origin_y}).
# Output: one JSON object on stdout, {"result":{...}} or {"error":{...}}.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class VmAgentDpi {
    [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr value);
    [DllImport("user32.dll")] public static extern bool IsWindow(IntPtr hwnd);
}
'@
# Per-Monitor V2 so BoundingRectangle is in physical pixels (spec D1).
[void][VmAgentDpi]::SetProcessDpiAwarenessContext([IntPtr]::new(-4))
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes

function Out-Json([object]$value) { [Console]::Out.Write($value); [Console]::Out.Flush() }

function Esc([string]$s) {
    if ($null -eq $s) { return '""' }
    $sb = [Text.StringBuilder]::new($s.Length + 2)
    [void]$sb.Append('"')
    foreach ($c in $s.ToCharArray()) {
        switch ($c) {
            '"' { [void]$sb.Append('\"') }
            '\' { [void]$sb.Append('\\') }
            "`n" { [void]$sb.Append('\n') }
            "`r" { [void]$sb.Append('\r') }
            "`t" { [void]$sb.Append('\t') }
            default {
                if ([int]$c -lt 0x20) { [void]$sb.AppendFormat('\u{0:x4}', [int]$c) } else { [void]$sb.Append($c) }
            }
        }
    }
    [void]$sb.Append('"')
    return $sb.ToString()
}

function Fail([string]$code, [string]$message) {
    Out-Json ('{"error":{"code":' + (Esc $code) + ',"message":' + (Esc $message) + '}}')
    exit 0
}

try {
    $req = $env:VM_AGENT_A11Y_HELPER | ConvertFrom-Json
    $hwnd = [IntPtr]::new([Convert]::ToInt64(($req.target.id -replace '^0x', ''), 16))
    if (-not [VmAgentDpi]::IsWindow($hwnd)) { Fail 'not_found' "window $($req.target.id) does not exist" }
    $root = [Windows.Automation.AutomationElement]::FromHandle($hwnd)
} catch {
    Fail 'io_failed' $_.Exception.Message
}

$walker = [Windows.Automation.TreeWalker]::ControlViewWalker
$script:nodes = 0
$script:truncated = $false
$maxDepth = [int]$req.max_depth
$maxNodes = [int]$req.max_nodes
$ox = [int]$req.origin_x
$oy = [int]$req.origin_y
$P = [Windows.Automation.AutomationElement]

function Snake([string]$s) {
    return (($s -creplace '([a-z0-9])([A-Z])', '$1_$2') -replace '[\s-]+', '_').ToLowerInvariant()
}

function Node([Windows.Automation.AutomationElement]$e, [int]$depth) {
    $sb = [Text.StringBuilder]::new()
    $role = ''
    $name = ''
    $value = $null
    $rect = 'null'
    $states = [Collections.Generic.List[string]]::new()
    try {
        $info = $e.Current
        $role = Snake ($info.ControlType.ProgrammaticName -replace '^ControlType\.', '')
        $name = $info.Name
        if ($info.IsEnabled) { $states.Add('enabled') }
        if ($info.IsKeyboardFocusable) { $states.Add('focusable') }
        if ($info.HasKeyboardFocus) { $states.Add('focused') }
        if ($info.IsOffscreen) { $states.Add('offscreen') }
        $r = $info.BoundingRectangle
        if (-not $r.IsEmpty -and $r.Width -gt 0 -and $r.Height -gt 0) {
            $rect = '{{"x":{0},"y":{1},"w":{2},"h":{3}}}' -f ([int]$r.X - $ox), ([int]$r.Y - $oy), [int]$r.Width, [int]$r.Height
        }
        $pattern = $null
        if ($e.TryGetCurrentPattern([Windows.Automation.ValuePattern]::Pattern, [ref]$pattern)) {
            $value = $pattern.Current.Value
            if ($pattern.Current.IsReadOnly) { $states.Add('read_only') } else { $states.Add('editable') }
        } elseif ($e.TryGetCurrentPattern([Windows.Automation.RangeValuePattern]::Pattern, [ref]$pattern)) {
            $value = [string]$pattern.Current.Value
        }
        if ($e.TryGetCurrentPattern([Windows.Automation.TogglePattern]::Pattern, [ref]$pattern)) {
            if ($pattern.Current.ToggleState -eq 'On') { $states.Add('checked') }
        }
        if ($e.TryGetCurrentPattern([Windows.Automation.SelectionItemPattern]::Pattern, [ref]$pattern)) {
            if ($pattern.Current.IsSelected) { $states.Add('selected') }
        }
        if ($e.TryGetCurrentPattern([Windows.Automation.ExpandCollapsePattern]::Pattern, [ref]$pattern)) {
            switch ($pattern.Current.ExpandCollapseState) {
                'Expanded' { $states.Add('expanded') }
                'Collapsed' { $states.Add('collapsed') }
            }
        }
        if ($e.TryGetCurrentPattern([Windows.Automation.WindowPattern]::Pattern, [ref]$pattern)) {
            if ($pattern.Current.IsModal) { $states.Add('modal') }
        }
    } catch {
        # The element vanished while being read; report what was gathered.
    }
    if ($null -ne $value -and $value.Length -gt 4096) { $value = $value.Substring(0, 4096) }
    $states.Sort([StringComparer]::Ordinal)
    [void]$sb.Append('{"role":').Append((Esc $role)).Append(',"name":').Append((Esc $name))
    if ($null -ne $value) { [void]$sb.Append(',"value":').Append((Esc $value)) }
    [void]$sb.Append(',"rect":').Append($rect).Append(',"state":[')
    [void]$sb.Append((($states | ForEach-Object { Esc $_ }) -join ',')).Append('],"children":[')
    $child = $null
    try { $child = $walker.GetFirstChild($e) } catch { }
    $first = $true
    while ($null -ne $child) {
        if ($depth -ge $maxDepth -or $script:nodes -ge $maxNodes) { $script:truncated = $true; break }
        $script:nodes++
        if (-not $first) { [void]$sb.Append(',') }
        $first = $false
        [void]$sb.Append((Node $child ($depth + 1)))
        try { $child = $walker.GetNextSibling($child) } catch { $child = $null }
    }
    [void]$sb.Append(']}')
    return $sb.ToString()
}

$script:nodes = 1
$tree = Node $root 1
Out-Json ('{"result":{"root":' + $tree + ',"nodes":' + $script:nodes + ',"truncated":' + $script:truncated.ToString().ToLowerInvariant() + '}}')
