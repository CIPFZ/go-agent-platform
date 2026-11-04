package a11y

import (
	"os"
	"testing"
)

// The test binary doubles as the Linux helper, like vm-agent itself.
func TestMain(m *testing.M) {
	if IsHelper() {
		os.Exit(HelperMain())
	}
	os.Exit(m.Run())
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{"push button": "push_button", "ListItem": "list_item", "frame": "frame", "Menu Bar": "menu_bar", "page-tab": "page_tab", "TitleBar": "title_bar"} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBudget(t *testing.T) {
	b := budget{maxNodes: 2}
	if !b.take() || !b.take() || b.take() || !b.truncated || b.nodes != 2 {
		t.Fatalf("budget = %+v", b)
	}
}
