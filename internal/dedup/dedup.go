// Package dedup implements request_id deduplication for non-retryable actions
// (spec section 5.6): an in-flight set plus a bounded, windowed cache of
// completed JSON responses keyed by method + path + request_id.
package dedup

import (
	"container/list"
	"sync"
	"time"
)

// DefaultCapacity is the cache bound of spec section 7.
const DefaultCapacity = 10000

// State is the outcome of Begin.
type State int

const (
	// Claimed means the caller owns the key and must call Finish or FinishStream.
	Claimed State = iota
	// InFlight means the first request with this key is still executing.
	InFlight
	// Done means a completed JSON response is cached and must be replayed.
	Done
	// Stream means the first response was streamed and cannot be replayed.
	Stream
)

type entry struct {
	key      string
	inFlight bool
	stream   bool
	body     []byte
	expires  time.Time
	elem     *list.Element // position in the completed LRU; nil while in flight
}

// Cache is safe for concurrent use.
type Cache struct {
	mu       sync.Mutex
	window   time.Duration
	capacity int
	now      func() time.Time
	entries  map[string]*entry
	lru      *list.List // completed entries, oldest at the front
}

// New returns a cache that keeps completed responses for window and at most
// capacity of them (<=0 means DefaultCapacity).
func New(window time.Duration, capacity int, now func() time.Time) *Cache {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if now == nil {
		now = time.Now
	}
	return &Cache{window: window, capacity: capacity, now: now, entries: map[string]*entry{}, lru: list.New()}
}

// Begin claims key. For Done it also returns the cached body.
func (c *Cache) Begin(key string) (State, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.expire(now)
	if e, ok := c.entries[key]; ok {
		switch {
		case e.inFlight:
			return InFlight, nil
		case e.stream:
			return Stream, nil
		default:
			return Done, e.body
		}
	}
	c.entries[key] = &entry{key: key, inFlight: true}
	return Claimed, nil
}

// Finish records the JSON response body of a request claimed with Begin.
func (c *Cache) Finish(key string, body []byte) { c.complete(key, body, false) }

// FinishStream records that the response of key was streamed.
func (c *Cache) FinishStream(key string) { c.complete(key, nil, true) }

func (c *Cache) complete(key string, body []byte, stream bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !e.inFlight {
		return
	}
	e.inFlight, e.stream, e.body = false, stream, body
	e.expires = c.now().Add(c.window)
	e.elem = c.lru.PushBack(e)
	for c.lru.Len() > c.capacity {
		c.remove(c.lru.Front().Value.(*entry))
	}
}

// Len returns the number of cached and in-flight keys.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache) expire(now time.Time) {
	for front := c.lru.Front(); front != nil; front = c.lru.Front() {
		e := front.Value.(*entry)
		if now.Before(e.expires) {
			return
		}
		c.remove(e)
	}
}

func (c *Cache) remove(e *entry) {
	c.lru.Remove(e.elem)
	delete(c.entries, e.key)
}
