package capture

import (
	"net/http"
	"strings"
	"sync"

	"github.com/kuarezma/lazylocal/internal/model"
)

// MaxBodyBytes is the maximum number of body bytes retained in an Exchange.
const MaxBodyBytes = 64 * 1024 // 64 KB

// DefaultCapacity is the default ring buffer capacity.
const DefaultCapacity = 500

// RingBuffer is a thread-safe ring buffer storing recent HTTP exchanges.
type RingBuffer struct {
	mu       sync.RWMutex
	capacity int
	buf      []model.Exchange
	start    int // index of oldest item
	count    int // number of valid items
}

// NewRingBuffer creates a new RingBuffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &RingBuffer{
		capacity: capacity,
		buf:      make([]model.Exchange, capacity),
	}
}

// Record adds an Exchange into the ring buffer, overwriting the oldest if full.
func (r *RingBuffer) Record(ex model.Exchange) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.capacity <= 0 {
		return
	}

	if r.count < r.capacity {
		idx := (r.start + r.count) % r.capacity
		r.buf[idx] = ex
		r.count++
	} else {
		// Overwrite oldest
		r.buf[r.start] = ex
		r.start = (r.start + 1) % r.capacity
	}
}

// List returns captured exchanges from newest to oldest.
// If serviceID is non-empty, only exchanges matching that serviceID are returned.
// If onlyErrors is true, only exchanges with StatusCode >= 400 or a non-empty Error are returned.
func (r *RingBuffer) List(serviceID string, onlyErrors bool) []model.Exchange {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.count == 0 {
		return []model.Exchange{}
	}

	res := make([]model.Exchange, 0, r.count)
	for i := r.count - 1; i >= 0; i-- {
		idx := (r.start + i) % r.capacity
		item := r.buf[idx]

		if serviceID != "" && item.ServiceID != serviceID {
			continue
		}
		if onlyErrors {
			isErr := item.Resp.StatusCode >= 400 || item.Error != ""
			if !isErr {
				continue
			}
		}
		res = append(res, item)
	}

	return res
}

// Get returns the exchange with the given ID and true, or an empty Exchange and false.
func (r *RingBuffer) Get(exchangeID string) (model.Exchange, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for i := r.count - 1; i >= 0; i-- {
		idx := (r.start + i) % r.capacity
		item := r.buf[idx]
		if item.ID == exchangeID {
			return item, true
		}
	}
	return model.Exchange{}, false
}

// Clear removes exchanges. If serviceID is empty, all entries are removed.
// Otherwise, only entries matching serviceID are removed.
func (r *RingBuffer) Clear(serviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if serviceID == "" {
		r.buf = make([]model.Exchange, r.capacity)
		r.start = 0
		r.count = 0
		return
	}

	kept := make([]model.Exchange, 0, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.start + i) % r.capacity
		item := r.buf[idx]
		if item.ServiceID != serviceID {
			kept = append(kept, item)
		}
	}

	r.buf = make([]model.Exchange, r.capacity)
	for i, item := range kept {
		r.buf[i] = item
	}
	r.start = 0
	r.count = len(kept)
}

// MaskHeaders redacts sensitive headers (case-insensitive) to "REDACTED".
func MaskHeaders(h http.Header) map[string][]string {
	if h == nil {
		return make(map[string][]string)
	}
	masked := make(map[string][]string, len(h))
	for k, v := range h {
		lower := strings.ToLower(k)
		switch lower {
		case "authorization", "cookie", "set-cookie", "x-api-key", "proxy-authorization":
			masked[k] = []string{"REDACTED"}
		default:
			cp := make([]string, len(v))
			copy(cp, v)
			masked[k] = cp
		}
	}
	return masked
}
