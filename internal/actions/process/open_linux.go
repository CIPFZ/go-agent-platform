//go:build linux

package process

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoHandler means no desktop application is associated with the target.
var ErrNoHandler = errors.New("no application is associated with this file type or URL scheme")

// OpenResult reports what Open started.
type OpenResult struct {
	PID     int    `json:"pid,omitempty"`
	Handler string `json:"handler,omitempty"`
}

// Open opens a file path or URL with the user's default application, the
// way xdg-open does, without depending on the xdg-utils scripts (spec D3):
// it resolves the MIME type (x-scheme-handler/<scheme> for URLs), looks up
// the default desktop entry in mimeapps.list and mimeinfo.cache along the
// XDG directories, expands its Exec line and launches it detached.
func Open(target string, isURL bool, env []string) (OpenResult, error) {
	mime, arg, err := classify(target, isURL)
	if err != nil {
		return OpenResult{}, err
	}
	desktop, path, ok := defaultApp(mime)
	if !ok {
		return OpenResult{}, fmt.Errorf("%w (%s)", ErrNoHandler, mime)
	}
	argv, err := desktopExec(path, arg)
	if err != nil {
		return OpenResult{}, err
	}
	resolved, err := exec.LookPath(argv[0])
	if err != nil {
		return OpenResult{}, fmt.Errorf("handler %s: %w", desktop, err)
	}
	home, _ := os.UserHomeDir()
	pid, err := Launch(Command{Path: resolved, Args: argv, Dir: home, Env: env})
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{PID: pid, Handler: desktop}, nil
}

// classify returns the MIME type used for the lookup and the argument handed
// to the application.
func classify(target string, isURL bool) (mime, arg string, err error) {
	if isURL {
		u, err := url.Parse(target)
		if err != nil {
			return "", "", err
		}
		if u.Scheme != "file" {
			return "x-scheme-handler/" + strings.ToLower(u.Scheme), target, nil
		}
		target = u.Path
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "inode/directory", target, nil
	}
	return fileMIME(target), target, nil
}

func dataDirs() []string {
	home, _ := os.UserHomeDir()
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	dirs := []string{dataHome}
	sys := os.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(sys) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func configDirs() []string {
	home, _ := os.UserHomeDir()
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	dirs := []string{cfgHome}
	sys := os.Getenv("XDG_CONFIG_DIRS")
	if sys == "" {
		sys = "/etc/xdg"
	}
	for _, d := range filepath.SplitList(sys) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// fileMIME matches the file name against the shared-mime-info globs2
// database (highest weight, then longest pattern), sniffing the content as a
// fallback.
func fileMIME(path string) string {
	name := filepath.Base(path)
	lower := strings.ToLower(name)
	type match struct {
		weight  int
		pattern string
		mime    string
	}
	var best *match
	for _, dir := range dataDirs() {
		f, err := os.Open(filepath.Join(dir, "mime", "globs2"))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || line[0] == '#' {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) < 3 {
				continue
			}
			var weight int
			fmt.Sscan(parts[0], &weight)
			pattern := parts[2]
			caseSensitive := len(parts) > 3 && strings.Contains(parts[3], "cs")
			subject := lower
			if caseSensitive {
				subject = name
			} else {
				pattern = strings.ToLower(pattern)
			}
			if ok, _ := filepath.Match(pattern, subject); !ok {
				continue
			}
			m := match{weight, pattern, parts[1]}
			if best == nil || m.weight > best.weight || (m.weight == best.weight && len(m.pattern) > len(best.pattern)) {
				best = &m
			}
		}
		f.Close()
	}
	if best != nil {
		return best.mime
	}
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		ct := http.DetectContentType(buf[:n])
		if strings.HasPrefix(ct, "text/plain") {
			return "text/plain"
		}
		return strings.SplitN(ct, ";", 2)[0]
	}
	return "application/octet-stream"
}

// defaultApp finds the default desktop entry for mime following the XDG
// MIME applications specification: [Default Applications] in mimeapps.list
// (desktop-specific files first) across config and data directories, then
// the first installed application listed in mimeinfo.cache.
func defaultApp(mime string) (desktop, path string, ok bool) {
	var lists []string
	var desktops []string
	for _, d := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if d != "" {
			desktops = append(desktops, strings.ToLower(d))
		}
	}
	addLists := func(dir string) {
		for _, d := range desktops {
			lists = append(lists, filepath.Join(dir, d+"-mimeapps.list"))
		}
		lists = append(lists, filepath.Join(dir, "mimeapps.list"))
	}
	for _, d := range configDirs() {
		addLists(d)
	}
	for _, d := range dataDirs() {
		addLists(filepath.Join(d, "applications"))
	}
	for _, list := range lists {
		for _, candidate := range iniValues(list, "Default Applications", mime) {
			if p, ok := findDesktop(candidate); ok {
				return candidate, p, true
			}
		}
	}
	for _, d := range dataDirs() {
		for _, candidate := range iniValues(filepath.Join(d, "applications", "mimeinfo.cache"), "MIME Cache", mime) {
			if p, ok := findDesktop(candidate); ok {
				return candidate, p, true
			}
		}
	}
	return "", "", false
}

// iniValues returns the ;-separated values of key in section of an INI file.
func iniValues(path, section, key string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	in := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "["+section+"]"
			continue
		}
		if !in {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(k) == key {
			for _, item := range strings.Split(v, ";") {
				if item = strings.TrimSpace(item); item != "" {
					out = append(out, item)
				}
			}
		}
	}
	return out
}

// findDesktop resolves a desktop file id ("org.gnome.Foo.desktop", where
// "-" may stand for a subdirectory) under the applications directories.
func findDesktop(id string) (string, bool) {
	for _, d := range dataDirs() {
		base := filepath.Join(d, "applications")
		for _, rel := range []string{id, strings.ReplaceAll(id, "-", string(filepath.Separator))} {
			p := filepath.Join(base, rel)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p, true
			}
		}
	}
	return "", false
}

// desktopExec builds argv from the Exec key of a desktop entry, expanding
// the field codes of the Desktop Entry specification for one argument.
func desktopExec(path, arg string) ([]string, error) {
	execLine := ""
	for _, v := range iniValuesRaw(path, "Desktop Entry", "Exec") {
		execLine = v
	}
	if execLine == "" {
		return nil, fmt.Errorf("%s has no Exec key", filepath.Base(path))
	}
	tokens, err := splitExec(execLine)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	var argv []string
	used := false
	for _, tok := range tokens {
		switch tok {
		case "%f", "%F", "%u", "%U":
			argv = append(argv, arg)
			used = true
			continue
		case "%i", "%c", "%k", "%d", "%D", "%n", "%N", "%v", "%m":
			continue // deprecated or icon/name codes: drop
		}
		tok = strings.ReplaceAll(tok, "%%", "%")
		argv = append(argv, tok)
	}
	if !used {
		argv = append(argv, arg)
	}
	if len(argv) == 0 {
		return nil, errors.New("empty Exec line")
	}
	return argv, nil
}

func iniValuesRaw(path, section, key string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	in := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "["+section+"]"
			continue
		}
		if k, v, found := strings.Cut(line, "="); in && found && strings.TrimSpace(k) == key {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// splitExec tokenises an Exec value: whitespace separates arguments and
// double quotes group them, with \" \` \$ \\ escapes inside quotes.
func splitExec(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inQuote && ch == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case ch == '"':
			inQuote = !inQuote
			have = true
		case !inQuote && (ch == ' ' || ch == '\t'):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(ch)
			have = true
		}
	}
	if inQuote {
		return nil, errors.New("unterminated quote in Exec")
	}
	if have {
		out = append(out, cur.String())
	}
	return out, nil
}
