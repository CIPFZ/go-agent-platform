# 键名表

本文件由 `internal/actions/input/keys.go` 生成，测试 `TestKeysDocInSync` 保证两者一致。修改键表后运行：

```bash
go test ./internal/actions/input -run TestKeysDocInSync -update
```

用于 `/v1/input/key/press`、`key/down`、`key/up`、`key/hotkey` 及 batch 中对应动作的 `key`/`keys` 字段。键名不区分大小写；单个字符的标点（如 `-`）按别名解析。未知键名返回 `invalid_request`，`details.reason=unknown_key`。

映射规则：

- Windows：通过 `SendInput` 发送虚拟键码（VK）并携带 set-1 扫描码；“扩展”列为是的键附带 `KEYEVENTF_EXTENDEDKEY`。VK 与布局无关，因此字母、数字、标点键按物理位置（美式布局）命名。
- Linux X11：通过 XTest 发送 keysym 对应的 keycode；当前布局中没有该 keysym 时临时绑定到空闲 keycode，请求结束后恢复。
- Linux Wayland（阶段 3）：通过 uinput 虚拟设备发送 evdev 键码，由 compositor 按其键盘布局解释；evdev 键码按美式布局位置命名，与扫描码列相同的键见下表。
- `key/type` 不使用本表：`method=unicode`（默认）在 Windows 用 `KEYEVENTF_UNICODE`、在 Linux 用字符的 keysym（Latin-1 直接映射，其余为 `0x01000000 + 码点`）；`method=scancode` 仅支持可打印 ASCII、制表符与换行，按当前布局的物理键输入，用于不响应 Unicode 注入的程序。文本中的 `\n`（含 `\r\n`）按 Enter，`\t` 按 Tab。

注意：

- 单独按下并释放 `win`（`super`）会打开系统开始菜单或活动概览，请在需要组合键时使用 `key/hotkey`（例如 `["win","r"]`）。
- `ralt`（`altgr`）在部分布局中等同 AltGr；`ctrl`+`alt`+`delete` 无法通过注入触发安全注意序列。
- `key/down` 按下的键在 `key/up` 之前保持按下；`key/hotkey` 与 `key/press` 中途失败时，本次已按下的键会被释放。

## 字母

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `a` | 0x41 | 0x1E | 否 | 0x0061 | 30 |
| `b` | 0x42 | 0x30 | 否 | 0x0062 | 48 |
| `c` | 0x43 | 0x2E | 否 | 0x0063 | 46 |
| `d` | 0x44 | 0x20 | 否 | 0x0064 | 32 |
| `e` | 0x45 | 0x12 | 否 | 0x0065 | 18 |
| `f` | 0x46 | 0x21 | 否 | 0x0066 | 33 |
| `g` | 0x47 | 0x22 | 否 | 0x0067 | 34 |
| `h` | 0x48 | 0x23 | 否 | 0x0068 | 35 |
| `i` | 0x49 | 0x17 | 否 | 0x0069 | 23 |
| `j` | 0x4A | 0x24 | 否 | 0x006a | 36 |
| `k` | 0x4B | 0x25 | 否 | 0x006b | 37 |
| `l` | 0x4C | 0x26 | 否 | 0x006c | 38 |
| `m` | 0x4D | 0x32 | 否 | 0x006d | 50 |
| `n` | 0x4E | 0x31 | 否 | 0x006e | 49 |
| `o` | 0x4F | 0x18 | 否 | 0x006f | 24 |
| `p` | 0x50 | 0x19 | 否 | 0x0070 | 25 |
| `q` | 0x51 | 0x10 | 否 | 0x0071 | 16 |
| `r` | 0x52 | 0x13 | 否 | 0x0072 | 19 |
| `s` | 0x53 | 0x1F | 否 | 0x0073 | 31 |
| `t` | 0x54 | 0x14 | 否 | 0x0074 | 20 |
| `u` | 0x55 | 0x16 | 否 | 0x0075 | 22 |
| `v` | 0x56 | 0x2F | 否 | 0x0076 | 47 |
| `w` | 0x57 | 0x11 | 否 | 0x0077 | 17 |
| `x` | 0x58 | 0x2D | 否 | 0x0078 | 45 |
| `y` | 0x59 | 0x15 | 否 | 0x0079 | 21 |
| `z` | 0x5A | 0x2C | 否 | 0x007a | 44 |

## 数字

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `0` | 0x30 | 0x0B | 否 | 0x0030 | 11 |
| `1` | 0x31 | 0x02 | 否 | 0x0031 | 2 |
| `2` | 0x32 | 0x03 | 否 | 0x0032 | 3 |
| `3` | 0x33 | 0x04 | 否 | 0x0033 | 4 |
| `4` | 0x34 | 0x05 | 否 | 0x0034 | 5 |
| `5` | 0x35 | 0x06 | 否 | 0x0035 | 6 |
| `6` | 0x36 | 0x07 | 否 | 0x0036 | 7 |
| `7` | 0x37 | 0x08 | 否 | 0x0037 | 8 |
| `8` | 0x38 | 0x09 | 否 | 0x0038 | 9 |
| `9` | 0x39 | 0x0A | 否 | 0x0039 | 10 |

## 功能键

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `f1` | 0x70 | 0x3B | 否 | 0xffbe | 59 |
| `f10` | 0x79 | 0x44 | 否 | 0xffc7 | 68 |
| `f11` | 0x7A | 0x57 | 否 | 0xffc8 | 87 |
| `f12` | 0x7B | 0x58 | 否 | 0xffc9 | 88 |
| `f13` | 0x7C | 0x64 | 否 | 0xffca | 183 |
| `f14` | 0x7D | 0x65 | 否 | 0xffcb | 184 |
| `f15` | 0x7E | 0x66 | 否 | 0xffcc | 185 |
| `f16` | 0x7F | 0x67 | 否 | 0xffcd | 186 |
| `f17` | 0x80 | 0x68 | 否 | 0xffce | 187 |
| `f18` | 0x81 | 0x69 | 否 | 0xffcf | 188 |
| `f19` | 0x82 | 0x6A | 否 | 0xffd0 | 189 |
| `f2` | 0x71 | 0x3C | 否 | 0xffbf | 60 |
| `f20` | 0x83 | 0x6B | 否 | 0xffd1 | 190 |
| `f21` | 0x84 | 0x6C | 否 | 0xffd2 | 191 |
| `f22` | 0x85 | 0x6D | 否 | 0xffd3 | 192 |
| `f23` | 0x86 | 0x6E | 否 | 0xffd4 | 193 |
| `f24` | 0x87 | 0x76 | 否 | 0xffd5 | 194 |
| `f3` | 0x72 | 0x3D | 否 | 0xffc0 | 61 |
| `f4` | 0x73 | 0x3E | 否 | 0xffc1 | 62 |
| `f5` | 0x74 | 0x3F | 否 | 0xffc2 | 63 |
| `f6` | 0x75 | 0x40 | 否 | 0xffc3 | 64 |
| `f7` | 0x76 | 0x41 | 否 | 0xffc4 | 65 |
| `f8` | 0x77 | 0x42 | 否 | 0xffc5 | 66 |
| `f9` | 0x78 | 0x43 | 否 | 0xffc6 | 67 |

## 修饰键

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `alt` | 0xA4 | 0x38 | 否 | 0xffe9 | 56 |
| `ctrl` | 0xA2 | 0x1D | 否 | 0xffe3 | 29 |
| `ralt` | 0xA5 | 0x38 | 是 | 0xffea | 100 |
| `rctrl` | 0xA3 | 0x1D | 是 | 0xffe4 | 97 |
| `rshift` | 0xA1 | 0x36 | 否 | 0xffe2 | 54 |
| `rwin` | 0x5C | 0x5C | 是 | 0xffec | 126 |
| `shift` | 0xA0 | 0x2A | 否 | 0xffe1 | 42 |
| `win` | 0x5B | 0x5B | 是 | 0xffeb | 125 |

## 导航键

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `down` | 0x28 | 0x50 | 是 | 0xff54 | 108 |
| `end` | 0x23 | 0x4F | 是 | 0xff57 | 107 |
| `home` | 0x24 | 0x47 | 是 | 0xff50 | 102 |
| `left` | 0x25 | 0x4B | 是 | 0xff51 | 105 |
| `pagedown` | 0x22 | 0x51 | 是 | 0xff56 | 109 |
| `pageup` | 0x21 | 0x49 | 是 | 0xff55 | 104 |
| `right` | 0x27 | 0x4D | 是 | 0xff53 | 106 |
| `up` | 0x26 | 0x48 | 是 | 0xff52 | 103 |

## 编辑与系统键

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `backspace` | 0x08 | 0x0E | 否 | 0xff08 | 14 |
| `capslock` | 0x14 | 0x3A | 否 | 0xffe5 | 58 |
| `delete` | 0x2E | 0x53 | 是 | 0xffff | 111 |
| `enter` | 0x0D | 0x1C | 否 | 0xff0d | 28 |
| `escape` | 0x1B | 0x01 | 否 | 0xff1b | 1 |
| `insert` | 0x2D | 0x52 | 是 | 0xff63 | 110 |
| `menu` | 0x5D | 0x5D | 是 | 0xff67 | 127 |
| `numlock` | 0x90 | 0x45 | 是 | 0xff7f | 69 |
| `pause` | 0x13 | 0x45 | 否 | 0xff13 | 119 |
| `printscreen` | 0x2C | 0x37 | 是 | 0xff61 | 99 |
| `scrolllock` | 0x91 | 0x46 | 否 | 0xff14 | 70 |
| `space` | 0x20 | 0x39 | 否 | 0x0020 | 57 |
| `tab` | 0x09 | 0x0F | 否 | 0xff09 | 15 |

## 标点（美式布局位置）

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `backquote` | 0xC0 | 0x29 | 否 | 0x0060 | 41 |
| `backslash` | 0xDC | 0x2B | 否 | 0x005c | 43 |
| `bracketleft` | 0xDB | 0x1A | 否 | 0x005b | 26 |
| `bracketright` | 0xDD | 0x1B | 否 | 0x005d | 27 |
| `comma` | 0xBC | 0x33 | 否 | 0x002c | 51 |
| `equal` | 0xBB | 0x0D | 否 | 0x003d | 13 |
| `minus` | 0xBD | 0x0C | 否 | 0x002d | 12 |
| `period` | 0xBE | 0x34 | 否 | 0x002e | 52 |
| `quote` | 0xDE | 0x28 | 否 | 0x0027 | 40 |
| `semicolon` | 0xBA | 0x27 | 否 | 0x003b | 39 |
| `slash` | 0xBF | 0x35 | 否 | 0x002f | 53 |

## 小键盘

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `numpad0` | 0x60 | 0x52 | 否 | 0xffb0 | 82 |
| `numpad1` | 0x61 | 0x4F | 否 | 0xffb1 | 79 |
| `numpad2` | 0x62 | 0x50 | 否 | 0xffb2 | 80 |
| `numpad3` | 0x63 | 0x51 | 否 | 0xffb3 | 81 |
| `numpad4` | 0x64 | 0x4B | 否 | 0xffb4 | 75 |
| `numpad5` | 0x65 | 0x4C | 否 | 0xffb5 | 76 |
| `numpad6` | 0x66 | 0x4D | 否 | 0xffb6 | 77 |
| `numpad7` | 0x67 | 0x47 | 否 | 0xffb7 | 71 |
| `numpad8` | 0x68 | 0x48 | 否 | 0xffb8 | 72 |
| `numpad9` | 0x69 | 0x49 | 否 | 0xffb9 | 73 |
| `numpad_add` | 0x6B | 0x4E | 否 | 0xffab | 78 |
| `numpad_decimal` | 0x6E | 0x53 | 否 | 0xffae | 83 |
| `numpad_divide` | 0x6F | 0x35 | 是 | 0xffaf | 98 |
| `numpad_enter` | 0x0D | 0x1C | 是 | 0xff8d | 96 |
| `numpad_multiply` | 0x6A | 0x37 | 否 | 0xffaa | 55 |
| `numpad_subtract` | 0x6D | 0x4A | 否 | 0xffad | 74 |

## 媒体键

| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |
| --- | --- | --- | --- | --- | --- |
| `media_next` | 0xB0 | 0x19 | 是 | 0x1008ff17 | 163 |
| `media_play_pause` | 0xB3 | 0x22 | 是 | 0x1008ff14 | 164 |
| `media_prev` | 0xB1 | 0x10 | 是 | 0x1008ff16 | 165 |
| `media_stop` | 0xB2 | 0x24 | 是 | 0x1008ff15 | 166 |
| `volume_down` | 0xAE | 0x2E | 是 | 0x1008ff11 | 114 |
| `volume_mute` | 0xAD | 0x20 | 是 | 0x1008ff12 | 113 |
| `volume_up` | 0xAF | 0x30 | 是 | 0x1008ff13 | 115 |

## 别名

| 别名 | 键名 |
| --- | --- |
| `'` | `quote` |
| `,` | `comma` |
| `-` | `minus` |
| `.` | `period` |
| `/` | `slash` |
| `;` | `semicolon` |
| `=` | `equal` |
| `[` | `bracketleft` |
| `\` | `backslash` |
| `]` | `bracketright` |
| `` ` `` | `backquote` |
| `altgr` | `ralt` |
| `apps` | `menu` |
| `back` | `backspace` |
| `break` | `pause` |
| `caps` | `capslock` |
| `cmd` | `win` |
| `control` | `ctrl` |
| `del` | `delete` |
| `esc` | `escape` |
| `grave` | `backquote` |
| `ins` | `insert` |
| `kp_enter` | `numpad_enter` |
| `lalt` | `alt` |
| `lcontrol` | `ctrl` |
| `lctrl` | `ctrl` |
| `lshift` | `shift` |
| `lwin` | `win` |
| `meta` | `win` |
| `mute` | `volume_mute` |
| `num_enter` | `numpad_enter` |
| `option` | `alt` |
| `pgdn` | `pagedown` |
| `pgup` | `pageup` |
| `play_pause` | `media_play_pause` |
| `print` | `printscreen` |
| `prtsc` | `printscreen` |
| `return` | `enter` |
| `rsuper` | `rwin` |
| `spacebar` | `space` |
| `super` | `win` |
