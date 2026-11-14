package dashboard

import (
	"net/http"
	"sync"
)

// Request limits (ADR-023 section 4, T7): at most maxDataRequests requests that read
// the data source (the list, the detail, the turn page and the polling JSON) are
// served at once, and at most maxInFlight requests of any kind (those and the audit
// feed and the static assets) are in flight across the server. A request over a limit
// is refused at once with 503 and Retry-After: it is never queued and never waits.
const (
	maxDataRequests = 4
	maxInFlight     = 8
	busyText        = "busy"
	busyRetryAfter  = "1"
)

type limiter struct {
	mu       sync.Mutex
	inFlight int
	data     int
}

// acquire takes a slot without waiting. data marks a request that reads the data
// source. The returned function releases the slot exactly once.
func (l *limiter) acquire(data bool) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight >= maxInFlight || (data && l.data >= maxDataRequests) {
		return nil, false
	}
	l.inFlight++
	if data {
		l.data++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.inFlight--
			if data {
				l.data--
			}
			l.mu.Unlock()
		})
	}, true
}

// refuseBusy answers a request over a limit.
func refuseBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", busyRetryAfter)
	http.Error(w, busyText, http.StatusServiceUnavailable)
}
