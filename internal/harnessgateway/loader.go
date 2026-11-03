package harnessgateway

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultProfileReadTimeout = 5 * time.Second

// Loader reads the profiles ConfigMap with a short TTL cache, so a new profile
// takes effect with no restart. Only this one ConfigMap in the Sympozium
// namespace counts: nothing an agent or a tenant writes can add a profile.
//
// It must be constructed with an UNCACHED reader, as pricing.Loader is.
type Loader struct {
	Reader    client.Reader
	Name      string
	Namespace string
	TTL       time.Duration
	// ReadTimeout limits one read of the ConfigMap.
	ReadTimeout time.Duration
	// OnRejected is called once per load for each skipped entry.
	OnRejected func(string)

	mu        sync.Mutex
	set       *Set
	err       error
	fetchedAt time.Time
	reading   chan struct{} // closed when the read in progress ends
}

// Load returns the current profiles. Callers must fail closed on an error.
//
// One read runs at a time, and callers wait for it without holding the lock, so
// a caller can hang up while it waits. The read doesn't use the caller's context:
// one caller hanging up must not fail the others. A call never gets a set older
// than the TTL, so a removed profile stops working on time. Calls wait for the
// read instead, for at most ReadTimeout.
func (l *Loader) Load(ctx context.Context) (*Set, error) {
	l.mu.Lock()
	ttl := l.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if !l.fetchedAt.IsZero() && time.Since(l.fetchedAt) < ttl {
		defer l.mu.Unlock()
		return l.set, l.err
	}
	if l.reading == nil {
		l.reading = make(chan struct{})
		go l.read(context.WithoutCancel(ctx))
	}
	reading := l.reading
	l.mu.Unlock()

	select {
	case <-reading:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.set, l.err
}

// read replaces the set. A failed read is not cached, because it says nothing
// about the next one. A file that doesn't parse is, until the TTL ends.
func (l *Loader) read(ctx context.Context) {
	timeout := l.ReadTimeout
	if timeout <= 0 {
		timeout = defaultProfileReadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var fetchedAt time.Time
	var set *Set
	var cm corev1.ConfigMap
	err := l.Reader.Get(ctx, types.NamespacedName{Name: l.Name, Namespace: l.Namespace}, &cm)
	if err == nil {
		set, err = ParseSet([]byte(cm.Data[DataKey]))
		fetchedAt = time.Now()
		if err == nil && l.OnRejected != nil {
			for _, rejected := range set.Rejected {
				l.OnRejected(rejected)
			}
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.set, l.err, l.fetchedAt = set, err, fetchedAt
	close(l.reading)
	l.reading = nil
}
