package guard

import (
	"container/list"
	"math"
	"net/netip"
	"sync"
	"time"
)

// limiter keeps one token bucket per key. Buckets for a whole rule are few
// and fixed by the config, so they are kept apart and never dropped. The
// rest live in a table of fixed size. To make room, only a bucket that has
// refilled completely is dropped, since losing it changes nothing; when no
// such bucket is near the back of the table, a new key is refused instead,
// so a flood of new addresses can't reset someone else's limit or lockout.
type limiter struct {
	mu      sync.Mutex
	size    int
	order   *list.List // front: most recently used
	buckets map[string]*list.Element
	fixed   map[string]*bucket

	onEvict func() // a refilled bucket made room
	onFull  func() // a new key was refused: nothing could make room
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
	rate   Rate
}

// evictScan is how many buckets from the back of the table are looked at
// for one that has refilled.
const evictScan = 32

func newLimiter(size int) *limiter {
	return &limiter{size: size, order: list.New(), buckets: map[string]*list.Element{}, fixed: map[string]*bucket{}}
}

func (b *bucket) refill(now time.Time) {
	perSec := float64(b.rate.Count) / b.rate.Period.Seconds()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(float64(b.rate.Burst), b.tokens+elapsed*perSec)
		b.last = now
	}
}

func (b *bucket) wait() time.Duration {
	perSec := float64(b.rate.Count) / b.rate.Period.Seconds()
	return time.Duration((1 - b.tokens) / perSec * float64(time.Second))
}

// take spends one token from key's bucket. When none is left, or the key
// can't get a bucket, it returns false and how long to wait.
func (l *limiter) take(key string, r Rate, now time.Time, fixed bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(key, r, now, fixed, true)
	if b == nil {
		return false, r.Period / time.Duration(r.Count)
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, b.wait()
}

// refund gives back a token taken with take.
func (l *limiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.buckets[key]; ok {
		b := e.Value.(*bucket)
		b.tokens = math.Min(float64(b.rate.Burst), b.tokens+1)
	}
}

func (l *limiter) get(key string, r Rate, now time.Time, fixed, create bool) *bucket {
	if fixed {
		b := l.fixed[key]
		if b == nil {
			b = &bucket{key: key, tokens: float64(r.Burst), last: now, rate: r}
			l.fixed[key] = b
		}
		b.refill(now)
		return b
	}
	if e, ok := l.buckets[key]; ok {
		l.order.MoveToFront(e)
		b := e.Value.(*bucket)
		b.refill(now)
		return b
	}
	if !create {
		return nil
	}
	if l.order.Len() >= l.size && !l.makeRoom(now) {
		if l.onFull != nil {
			l.onFull()
		}
		return nil
	}
	b := &bucket{key: key, tokens: float64(r.Burst), last: now, rate: r}
	l.buckets[key] = l.order.PushFront(b)
	return b
}

// makeRoom drops one bucket that has refilled completely, looking from the
// least recently used end.
func (l *limiter) makeRoom(now time.Time) bool {
	e := l.order.Back()
	for i := 0; e != nil && i < evictScan; i++ {
		b := e.Value.(*bucket)
		b.refill(now)
		if b.tokens >= float64(b.rate.Burst) {
			l.order.Remove(e)
			delete(l.buckets, b.key)
			if l.onEvict != nil {
				l.onEvict()
			}
			return true
		}
		e = e.Prev()
	}
	return false
}

// len is how many buckets the table holds, for tests.
func (l *limiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// addrKey is the part of an address that limits count: the whole address for
// IPv4, and the /64 network for IPv6, since one machine usually has a whole
// /64 to send from.
func addrKey(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() || !a.IsValid() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}
