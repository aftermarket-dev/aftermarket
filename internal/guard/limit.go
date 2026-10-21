package guard

import (
	"container/list"
	"math"
	"net/netip"
	"sync"
	"time"
)

// limiter keeps one token bucket per key in a table of fixed size. When the
// table is full, the bucket used longest ago makes room, so a client can't
// grow memory by sending from ever more addresses.
type limiter struct {
	mu      sync.Mutex
	size    int
	order   *list.List // front: most recently used
	buckets map[string]*list.Element
	evicted uint64
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
}

func newLimiter(size int) *limiter {
	return &limiter{size: size, order: list.New(), buckets: map[string]*list.Element{}}
}

// take spends one token from key's bucket. When none is left it returns
// false and how long until one is.
func (l *limiter) take(key string, r Rate, now time.Time) (bool, time.Duration) {
	return l.spend(key, r, now, true)
}

// peek reports whether key's bucket has a token, without spending one.
func (l *limiter) peek(key string, r Rate, now time.Time) (bool, time.Duration) {
	return l.spend(key, r, now, false)
}

func (l *limiter) spend(key string, r Rate, now time.Time, spend bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	perSec := float64(r.Count) / r.Period.Seconds()
	var b *bucket
	if e, ok := l.buckets[key]; ok {
		l.order.MoveToFront(e)
		b = e.Value.(*bucket)
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens = math.Min(float64(r.Burst), b.tokens+elapsed*perSec)
		}
		b.last = now
	} else {
		if !spend {
			return true, 0 // a full bucket nobody has used
		}
		for l.order.Len() >= l.size {
			old := l.order.Back()
			l.order.Remove(old)
			delete(l.buckets, old.Value.(*bucket).key)
			l.evicted++
		}
		b = &bucket{key: key, tokens: float64(r.Burst), last: now}
		l.buckets[key] = l.order.PushFront(b)
	}
	if b.tokens >= 1 {
		if spend {
			b.tokens--
		}
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / perSec * float64(time.Second))
	return false, wait
}

// len is how many buckets are held, for tests.
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
