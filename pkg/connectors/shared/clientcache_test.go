package shared

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	id     int64
	closes atomic.Int32
}

func (f *fakeClient) Close() error {
	f.closes.Add(1)
	return nil
}

func (f *fakeClient) closed() bool { return f.closes.Load() > 0 }

// fakeFactory builds numbered clients and remembers every one it built, so a
// test can check that each was closed exactly once.
type fakeFactory struct {
	mu      sync.Mutex
	nextID  int64
	built   []*fakeClient
	release chan struct{} // when non-nil, build blocks until it is closed
}

func (f *fakeFactory) build() (*fakeClient, error) {
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	c := &fakeClient{id: f.nextID}
	f.built = append(f.built, c)
	return c, nil
}

func (f *fakeFactory) all() []*fakeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeClient(nil), f.built...)
}

func TestClientCache_InFlightRequestSurvivesReset(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](8)

	h, err := cache.Acquire("k", f.build)
	require.NoError(t, err)

	cache.Reset()

	assert.False(t, h.Client().closed(), "a reset must not close a client still in use")
	h.Release()
	assert.True(t, h.entry.client.closed(), "the retired client closes on its last release")
}

func TestClientCache_ResetPreventsReuseOfOldClient(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](8)

	h1, err := cache.Acquire("k", f.build)
	require.NoError(t, err)
	old := h1.Client()
	h1.Release()

	cache.Reset()
	assert.True(t, old.closed(), "an idle client is closed at once when retired")

	h2, err := cache.Acquire("k", f.build)
	require.NoError(t, err)
	defer h2.Release()
	assert.NotSame(t, old, h2.Client(), "after a reset the key gets a new client")
	assert.False(t, h2.Client().closed())
	assert.Len(t, f.all(), 2)
}

func TestClientCache_RetiredClientClosesAfterFinalRelease(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](8)

	h1, err := cache.Acquire("k", f.build)
	require.NoError(t, err)
	h2, err := cache.Acquire("k", f.build)
	require.NoError(t, err)
	client := h1.Client()
	require.Same(t, client, h2.Client())

	cache.Invalidate("k")
	assert.Equal(t, 0, cache.Len())

	h1.Release()
	assert.False(t, client.closed(), "one handle is still out")
	h2.Release()
	assert.True(t, client.closed())
	assert.Equal(t, int32(1), client.closes.Load())
}

func TestClientCache_ConcurrentUsersShareOneClient(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{release: make(chan struct{})}
	cache := NewClientCache[*fakeClient](8)

	const users = 32
	var wg sync.WaitGroup
	handles := make([]*ClientHandle[*fakeClient], users)
	for i := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := cache.Acquire("k", f.build)
			if err != nil {
				t.Error(err)
				return
			}
			handles[i] = h
		}()
	}
	close(f.release) // let every concurrent miss build at once
	wg.Wait()

	shared := handles[0].Client()
	for _, h := range handles {
		assert.Same(t, shared, h.Client(), "every user gets the stored client")
	}
	for _, c := range f.all() {
		if c != shared {
			assert.Equal(t, int32(1), c.closes.Load(), "a client that lost the race is closed unused")
		}
	}

	for _, h := range handles {
		h.Release()
	}
	assert.False(t, shared.closed(), "releasing does not close a client that is still cached")

	cache.Reset()
	assert.Equal(t, int32(1), shared.closes.Load())
}

func TestClientCache_RepeatedResetCyclesDoNotLeak(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](8)

	for i := range 500 {
		h, err := cache.Acquire(fmt.Sprintf("k%d", i%3), f.build)
		require.NoError(t, err)
		if i%2 == 0 {
			cache.Reset() // reset while held
			h.Release()
		} else {
			h.Release()
			cache.Reset() // reset while idle
		}
	}

	built := f.all()
	assert.Len(t, built, 500)
	for _, c := range built {
		assert.Equalf(t, int32(1), c.closes.Load(), "client %d", c.id)
	}
	assert.Equal(t, 0, cache.Len())
}

func TestClientCache_CloseCalledExactlyOnce(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](8)

	h, err := cache.Acquire("k", f.build)
	require.NoError(t, err)
	client := h.Client()

	cache.Reset()
	cache.Reset()
	cache.Invalidate("k")
	h.Release()
	h.Release() // a second release must not count again
	cache.Reset()

	assert.Equal(t, int32(1), client.closes.Load())
}

func TestClientCache_EvictionNeverClosesAClientInUse(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](1)

	held, err := cache.Acquire("a", f.build)
	require.NoError(t, err)

	h2, err := cache.Acquire("b", f.build) // full: evicts a, which is in use
	require.NoError(t, err)
	defer h2.Release()

	assert.False(t, held.Client().closed(), "an evicted client in use stays open")
	assert.Equal(t, 1, cache.Len())
	heldClient := held.Client()
	held.Release()
	assert.True(t, heldClient.closed(), "and is closed when its last handle is released")
}

// At the cap only the least recently acquired entry is evicted; hot clients
// stay cached.
func TestClientCache_OverflowEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](2)
	for _, key := range []string{"a", "b", "a"} { // a is now the most recent
		h, err := cache.Acquire(key, f.build)
		require.NoError(t, err)
		h.Release()
	}
	hb, err := cache.Acquire("b", f.build)
	require.NoError(t, err)
	bClient := hb.Client()
	hb.Release()
	ha, err := cache.Acquire("a", f.build)
	require.NoError(t, err)
	aClient := ha.Client()
	ha.Release()

	h, err := cache.Acquire("c", f.build) // full: evicts b, the least recent
	require.NoError(t, err)
	h.Release()

	assert.Equal(t, 2, cache.Len())
	assert.True(t, bClient.closed(), "the least recently used client is evicted and closed")
	assert.False(t, aClient.closed(), "the recently used client stays cached")
	again, err := cache.Acquire("a", f.build)
	require.NoError(t, err)
	assert.Same(t, aClient, again.Client())
	again.Release()
}

func TestClientCache_BuildErrorIsNotCached(t *testing.T) {
	t.Parallel()

	cache := NewClientCache[*fakeClient](8)
	boom := errors.New("boom")

	_, err := cache.Acquire("k", func() (*fakeClient, error) { return nil, boom })
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 0, cache.Len())

	f := &fakeFactory{}
	h, err := cache.Acquire("k", f.build)
	require.NoError(t, err)
	h.Release()
}

func TestClientHandle_ClientAfterReleasePanics(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](8)
	h, err := cache.Acquire("k", f.build)
	require.NoError(t, err)
	h.Release()

	assert.Panics(t, func() { _ = h.Client() })
}

// TestClientCache_ConcurrentAcquireReleaseReset runs acquires, releases, resets
// and invalidations together under -race. Any use of a client after it was
// closed, or any client left unclosed, fails the test.
func TestClientCache_ConcurrentAcquireReleaseReset(t *testing.T) {
	t.Parallel()

	f := &fakeFactory{}
	cache := NewClientCache[*fakeClient](4)
	var useAfterClose atomic.Int32

	var wg sync.WaitGroup
	for w := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				key := fmt.Sprintf("k%d", (w+i)%6)
				h, err := cache.Acquire(key, f.build)
				if err != nil {
					t.Error(err)
					return
				}
				c := h.Client()
				if c.closed() {
					useAfterClose.Add(1)
				}
				runtime.Gosched()
				if c.closed() {
					useAfterClose.Add(1)
				}
				h.Release()
				switch i % 25 {
				case 0:
					cache.Reset()
				case 7:
					cache.Invalidate(key)
				}
			}
		}()
	}
	wg.Wait()
	cache.Reset()

	assert.Zero(t, useAfterClose.Load(), "no caller may ever see a closed client")
	for _, c := range f.all() {
		assert.Equalf(t, int32(1), c.closes.Load(), "client %d must be closed exactly once", c.id)
	}
}
