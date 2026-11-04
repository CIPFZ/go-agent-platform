package input

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/keys.md from the key table")

const keysDocPath = "../../../docs/keys.md"

var keyGroups = []struct {
	title string
	match func(name string) bool
}{
	{"字母", func(n string) bool { return len(n) == 1 && n[0] >= 'a' && n[0] <= 'z' }},
	{"数字", func(n string) bool { return len(n) == 1 && n[0] >= '0' && n[0] <= '9' }},
	{"功能键", func(n string) bool { return len(n) >= 2 && n[0] == 'f' && n[1] >= '0' && n[1] <= '9' }},
	{"修饰键", func(n string) bool {
		return map[string]bool{"ctrl": true, "rctrl": true, "alt": true, "ralt": true, "shift": true, "rshift": true, "win": true, "rwin": true}[n]
	}},
	{"导航键", func(n string) bool {
		return map[string]bool{"up": true, "down": true, "left": true, "right": true, "home": true, "end": true, "pageup": true, "pagedown": true}[n]
	}},
	{"编辑与系统键", func(n string) bool {
		return map[string]bool{"enter": true, "tab": true, "space": true, "backspace": true, "delete": true, "insert": true, "escape": true,
			"capslock": true, "numlock": true, "scrolllock": true, "printscreen": true, "pause": true, "menu": true}[n]
	}},
	{"标点（美式布局位置）", func(n string) bool {
		return map[string]bool{"minus": true, "equal": true, "bracketleft": true, "bracketright": true, "backslash": true, "semicolon": true,
			"quote": true, "backquote": true, "comma": true, "period": true, "slash": true}[n]
	}},
	{"小键盘", func(n string) bool { return strings.HasPrefix(n, "numpad") }},
	{"媒体键", func(n string) bool { return strings.HasPrefix(n, "volume_") || strings.HasPrefix(n, "media_") }},
}

func renderKeysDoc() string {
	var b strings.Builder
	b.WriteString(`# 键名表

本文件由 ` + "`internal/actions/input/keys.go`" + ` 生成，测试 ` + "`TestKeysDocInSync`" + ` 保证两者一致。修改键表后运行：

` + "```bash\ngo test ./internal/actions/input -run TestKeysDocInSync -update\n```" + `

用于 ` + "`/v1/input/key/press`" + `、` + "`key/down`" + `、` + "`key/up`" + `、` + "`key/hotkey`" + ` 及 batch 中对应动作的 ` + "`key`/`keys`" + ` 字段。键名不区分大小写；单个字符的标点（如 ` + "`-`" + `）按别名解析。未知键名返回 ` + "`invalid_request`" + `，` + "`details.reason=unknown_key`" + `。

映射规则：

- Windows：通过 ` + "`SendInput`" + ` 发送虚拟键码（VK）并携带 set-1 扫描码；“扩展”列为是的键附带 ` + "`KEYEVENTF_EXTENDEDKEY`" + `。VK 与布局无关，因此字母、数字、标点键按物理位置（美式布局）命名。
- Linux X11：通过 XTest 发送 keysym 对应的 keycode；当前布局中没有该 keysym 时临时绑定到空闲 keycode，请求结束后恢复。
- Linux Wayland（阶段 3）：通过 uinput 虚拟设备发送 evdev 键码，由 compositor 按其键盘布局解释；evdev 键码按美式布局位置命名，与扫描码列相同的键见下表。
- ` + "`key/type`" + ` 不使用本表：` + "`method=unicode`" + `（默认）在 Windows 用 ` + "`KEYEVENTF_UNICODE`" + `、在 Linux 用字符的 keysym（Latin-1 直接映射，其余为 ` + "`0x01000000 + 码点`" + `）；` + "`method=scancode`" + ` 仅支持可打印 ASCII、制表符与换行，按当前布局的物理键输入，用于不响应 Unicode 注入的程序。文本中的 ` + "`\\n`" + `（含 ` + "`\\r\\n`" + `）按 Enter，` + "`\\t`" + ` 按 Tab。

注意：

- 单独按下并释放 ` + "`win`" + `（` + "`super`" + `）会打开系统开始菜单或活动概览，请在需要组合键时使用 ` + "`key/hotkey`" + `（例如 ` + "`[\"win\",\"r\"]`" + `）。
- ` + "`ralt`" + `（` + "`altgr`" + `）在部分布局中等同 AltGr；` + "`ctrl`+`alt`+`delete`" + ` 无法通过注入触发安全注意序列。
- ` + "`key/down`" + ` 按下的键在 ` + "`key/up`" + ` 之前保持按下；` + "`key/hotkey`" + ` 与 ` + "`key/press`" + ` 中途失败时，本次已按下的键会被释放。

`)
	used := map[string]bool{}
	for _, g := range keyGroups {
		fmt.Fprintf(&b, "## %s\n\n| 键名 | Windows VK | 扫描码 | 扩展 | X keysym | evdev |\n| --- | --- | --- | --- | --- | --- |\n", g.title)
		for _, name := range KeyNames() {
			if !g.match(name) || used[name] {
				continue
			}
			used[name] = true
			k := keyTable[name]
			ext := "否"
			if k.Ext {
				ext = "是"
			}
			fmt.Fprintf(&b, "| `%s` | 0x%02X | 0x%02X | %s | 0x%04x | %d |\n", name, k.VK, k.Scan, ext, k.Keysym, k.Evdev)
		}
		b.WriteString("\n")
	}
	b.WriteString("## 别名\n\n| 别名 | 键名 |\n| --- | --- |\n")
	aliases := make([]string, 0, len(keyAliases))
	for a := range keyAliases {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, a := range aliases {
		shown := a
		if a == "`" {
			shown = "`` ` ``"
		} else if a == "\\" {
			shown = "`\\`"
		} else {
			shown = "`" + a + "`"
		}
		fmt.Fprintf(&b, "| %s | `%s` |\n", shown, keyAliases[a])
	}
	for _, name := range KeyNames() {
		if !used[name] {
			panic("key " + name + " is in no group of keys.md")
		}
	}
	return b.String()
}

func TestKeysDocInSync(t *testing.T) {
	want := renderKeysDoc()
	path := filepath.Clean(keysDocPath)
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update)", path, err)
	}
	if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
		t.Fatalf("%s is out of date with keys.go; run go test ./internal/actions/input -run TestKeysDocInSync -update", path)
	}
}
