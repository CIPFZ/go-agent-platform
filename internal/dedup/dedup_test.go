package dedup

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestInFlightThenReplay(t *testing.T) {
	c := New(time.Minute, 0, nil)
	if state, _ := c.Begin("POST /v1/input/mouse/click a"); state != Claimed {
		t.Fatalf("first Begin = %v", state)
	}
	if state, _ := c.Begin("POST /v1/input/mouse/click a"); state != InFlight {
		t.Fatalf("concurrent Begin = %v, want InFlight", state)
	}
	c.Finish("POST /v1/input/mouse/click a", []byte(`{"ok":true}`))
	state, body := c.Begin("POST /v1/input/mouse/click a")
	if state != Done || string(body) != `{"ok":true}` {
		t.Fatalf("Begin after Finish = %v %q", state, body)
	}
}

func TestKeysAreIsolatedByMethodAndPath(t *testing.T) {
	c := New(time.Minute, 0, nil)
	c.Begin("POST /v1/input/mouse/click id")
	c.Finish("POST /v1/input/mouse/click id", []byte("click"))
	for _, key := range []string{"POST /v1/input/key/press id", "DELETE /v1/input/mouse/click id", "POST /v1/input/mouse/click other"} {
		if state, _ := c.Begin(key); state != Claimed {
			t.Errorf("Begin(%q) = %v, want Claimed", key, state)
		}
	}
}

func TestWindowExpiry(t *testing.T) {
	clk := &clock{t: time.Unix(0, 0)}
	c := New(time.Minute, 0, clk.now)
	c.Begin("k")
	c.Finish("k", []byte("x"))
	clk.t = clk.t.Add(59 * time.Second)
	if state, _ := c.Begin("k"); state != Done {
		t.Fatalf("inside window = %v", state)
	}
	clk.t = clk.t.Add(time.Second)
	if state, _ := c.Begin("k"); state != Claimed {
		t.Fatalf("after window = %v, want Claimed", state)
	}
}

func TestStreamMarker(t *testing.T) {
	c := New(time.Minute, 0, nil)
	c.Begin("k")
	c.FinishStream("k")
	if state, body := c.Begin("k"); state != Stream || body != nil {
		t.Fatalf("state = %v", state)
	}
}

func TestCapacityEvictsOldestCompleted(t *testing.T) {
	c := New(time.Hour, 3, nil)
	for i := 0; i < 5; i++ {
		key := fmt.Sprint(i)
		c.Begin(key)
		c.Finish(key, []byte(key))
	}
	c.Begin("inflight") // in-flight keys never count against the bound
	if c.Len() != 4 {
		t.Fatalf("len = %d, want 3 completed + 1 in flight", c.Len())
	}
	if state, _ := c.Begin("0"); state != Claimed {
		t.Fatalf("oldest key not evicted: %v", state)
	}
	if state, _ := c.Begin("4"); state != Done {
		t.Fatalf("newest key evicted: %v", state)
	}
}

func TestConcurrentBeginHasOneOwner(t *testing.T) {
	c := New(time.Minute, 0, nil)
	var owners sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for i := 0; i < 64; i++ {
		owners.Add(1)
		go func() {
			defer owners.Done()
			if state, _ := c.Begin("k"); state == Claimed {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}()
	}
	owners.Wait()
	if count != 1 {
		t.Fatalf("owners = %d", count)
	}
}
