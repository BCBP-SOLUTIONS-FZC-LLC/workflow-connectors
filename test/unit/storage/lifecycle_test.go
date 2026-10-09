package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

// closingClient is a provider client whose Fetch can be held open and which
// records closes and any use after close.
type closingClient struct {
	*storage.MockStorageClient
	entered chan struct{}
	proceed chan struct{}
	closes  atomic.Int32
}

func newClosingClient(blocking bool) *closingClient {
	c := &closingClient{MockStorageClient: storage.NewMockStorageClient()}
	if blocking {
		c.entered = make(chan struct{})
		c.proceed = make(chan struct{})
	}
	_ = c.Upload(context.Background(), "b", "k", []byte("data"), "text/plain")
	return c
}

func (c *closingClient) Fetch(ctx context.Context, bucket, key string, maxBytes int64) ([]byte, string, error) {
	if c.entered != nil {
		close(c.entered)
		<-c.proceed
	}
	if c.closes.Load() > 0 {
		return nil, "", errors.New("use after close")
	}
	return c.MockStorageClient.Fetch(ctx, bucket, key, maxBytes)
}

func (c *closingClient) Close() error {
	c.closes.Add(1)
	return nil
}

func TestStorage_ResetClients_InFlightCallSurvives_ThenOldClientCloses(t *testing.T) {
	t.Parallel()

	first := newClosingClient(true)
	second := newClosingClient(false)
	var built atomic.Int32
	conn := storage.New(map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			if built.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
	}, docref.NewMemoryService())

	done := make(chan error, 1)
	go func() {
		_, err := conn.Execute(tctx, base("fetch"))
		done <- err
	}()
	<-first.entered // the call holds the client

	conn.ResetClients()
	assert.Zero(t, first.closes.Load(), "a reset must not close a client an in-flight call is using")

	close(first.proceed)
	require.NoError(t, <-done, "the in-flight call completes on its client")
	assert.Equal(t, int32(1), first.closes.Load(), "the retired client closes once its call finishes")

	out, err := conn.Execute(tctx, base("fetch"))
	require.NoError(t, err)
	assert.Equal(t, "data", out["content"])
	assert.Equal(t, int32(2), built.Load(), "a call after the reset builds a new client")
	assert.Zero(t, second.closes.Load())
}

// TestStorage_ConcurrentCallsAcrossCacheOverflow drives more credential sets
// than the cache holds, from many goroutines, so overflow resets happen while
// other calls hold clients. No call may fail and every retired client must be
// closed exactly once.
func TestStorage_ConcurrentCallsAcrossCacheOverflow(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var clients []*closingClient
	conn := storage.New(map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
			c := newClosingClient(false)
			mu.Lock()
			clients = append(clients, c)
			mu.Unlock()
			return c, nil
		},
	}, docref.NewMemoryService())

	var wg sync.WaitGroup
	var failures atomic.Int32
	for w := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 60 {
				in := with(base("fetch"), "accessKey", fmt.Sprintf("tenant-%d-%d", w, i%40))
				if _, err := conn.Execute(tctx, in); err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	conn.ResetClients()

	assert.Zero(t, failures.Load(), "no call may fail because another goroutine reset the cache")
	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, len(clients), 256, "the test must overflow the cache")
	for i, c := range clients {
		assert.Equalf(t, int32(1), c.closes.Load(), "client %d must be closed exactly once", i)
	}
}
