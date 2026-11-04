package protocol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// reasonConstants parses errors.go for the Reason* string constants.
func reasonConstants(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Reason") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
					v, _ := strconv.Unquote(lit.Value)
					out[name.Name] = v
				}
			}
		}
	}
	return out
}

func TestErrorsDocListsEveryReason(t *testing.T) {
	doc, err := os.ReadFile("../../docs/errors.md")
	if err != nil {
		t.Fatal(err)
	}
	reasons := reasonConstants(t)
	if len(reasons) == 0 {
		t.Fatal("no Reason constants found")
	}
	documented := map[string]bool{}
	section := string(doc)
	start := strings.Index(section, "## details.reason")
	end := strings.Index(section, "## details 其他字段")
	if start < 0 || end < start {
		t.Fatal("errors.md lacks the reason table")
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)` \\|")
	for _, m := range row.FindAllStringSubmatch(section[start:end], -1) {
		documented[m[1]] = true
	}
	values := map[string]bool{}
	for name, v := range reasons {
		values[v] = true
		if !documented[v] {
			t.Errorf("%s = %q is not documented in docs/errors.md", name, v)
		}
	}
	for v := range documented {
		if !values[v] {
			t.Errorf("docs/errors.md documents reason %q that no Reason constant defines", v)
		}
	}
	for _, code := range []Code{CodeInvalidRequest, CodeNotFound, CodeAmbiguous, CodeConflict, CodeTooLarge, CodeUnsupported, CodeNoDisplay, CodeBusy, CodeTimeout, CodeIOFailed, CodeInternal} {
		line := "| `" + string(code) + "` | " + strconv.Itoa(code.HTTPStatus()) + " |"
		if !strings.Contains(section, line) {
			t.Errorf("errors.md code table lacks %q", line)
		}
	}
}

// Every details.reason must come from a Reason constant, so a literal string
// passed to WithReason anywhere in the module is a bug.
func TestNoLiteralReasons(t *testing.T) {
	literal := regexp.MustCompile(`WithReason\("|WithDetail\("reason"`)
	root := "../.."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "tools" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, filepath.Join("protocol", "errors.go")) {
			return nil
		}
		if literal.Match(data) {
			t.Errorf("%s sets a literal details.reason; add a Reason constant and document it", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCodeMapping(t *testing.T) {
	cases := map[Code][2]any{
		CodeInvalidRequest: {400, false}, CodeNotFound: {404, false}, CodeAmbiguous: {409, false},
		CodeConflict: {409, false}, CodeTooLarge: {413, false}, CodeUnsupported: {501, false},
		CodeNoDisplay: {503, true}, CodeBusy: {503, true}, CodeTimeout: {504, false},
		CodeIOFailed: {500, false}, CodeInternal: {500, false},
	}
	for code, want := range cases {
		if code.HTTPStatus() != want[0].(int) || code.Retryable() != want[1].(bool) {
			t.Errorf("%s: status %d retryable %v", code, code.HTTPStatus(), code.Retryable())
		}
	}
}
