package file

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteAtomicConcurrentCreateHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")
	const writers = 8
	var group sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	for i := 0; i < writers; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if _, err := WriteAtomic(path, strings.NewReader(strings.Repeat(string(rune('a'+i)), 128)), false); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			} else if !os.IsExist(err) {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	group.Wait()
	if succeeded != 1 {
		t.Fatalf("concurrent create successes = %d, want 1", succeeded)
	}
	data, _ := os.ReadFile(path)
	if len(data) != 128 || strings.Count(string(data), string(data[0])) != 128 {
		t.Fatalf("content is torn: %q", data)
	}
}

func TestWriteAtomicOverwriteAndNoLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if _, err := WriteAtomic(path, strings.NewReader("one"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAtomic(path, strings.NewReader("two"), false); !os.IsExist(err) {
		t.Fatalf("second create err = %v, want exist", err)
	}
	if n, err := WriteAtomic(path, strings.NewReader("three"), true); err != nil || n != 5 {
		t.Fatalf("overwrite n=%d err=%v", n, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "three" {
		t.Fatalf("content = %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
