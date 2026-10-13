package valkeystore_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/registry"
)

// The retry decision for a Valkey failure, end to end: a real valkeystore
// over a scripted node, behind docref.Service, behind connectors.New — the
// storage connector creating a ref (Put) and send-email resolving an
// attachment (Get) — then connectors.DecideRetry, as the worker calls it.

// refConnectors builds storage and send-email over a document-ref service
// whose metadata store is client. Neither provider is reached: every call
// fails on the ref store first.
func refConnectors(t *testing.T, client redis.UniversalClient) (store, email connectors.Connector) {
	t.Helper()
	bucket := storage.NewMockStorageClient()
	require.NoError(t, bucket.Upload(context.Background(), "b", "k", []byte("x"), "text/plain"))
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		DocRefs:       docref.NewService(valkeystore.New(client, valkeystore.Options{}), docref.NewMemoryContent("docs")),
		StorageProviders: map[string]storage.ProviderConstructor{
			"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return bucket, nil },
		},
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) {
				return sendemail.NewMockSendEmailClient(), nil
			},
		},
	})
	require.NoError(t, err)
	return all[registry.TypeStorage], all[registry.TypeSendEmail]
}

var refCtx = connectors.WithTenant(context.Background(), "t")

func createDocument(conn connectors.Connector) error {
	_, err := conn.Execute(refCtx, map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "b", "key": "k", "createDocument": true,
	})
	return err
}

func sendWithAttachment(conn connectors.Connector) (map[string]any, error) {
	return conn.Execute(refCtx, map[string]any{
		"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com",
		"body": "x", "attachments": []any{docref.NewID()},
	})
}

// BUSY, a server at maxclients, an exhausted pool and a closed cluster node
// client are docref.ErrUnavailable: storage retries them (safe), and
// send-email retries them too, since resolving an attachment fails before
// anything is sent (not delivered, transient).
func TestRetryDecision_ValkeyUnavailable_StorageAndEmailRetried(t *testing.T) {
	t.Parallel()
	refusal := func(reply string) func(*testing.T) redis.UniversalClient {
		return func(t *testing.T) redis.UniversalClient {
			return fakeClient(t, func([]string) string { return reply })
		}
	}
	for name, client := range map[string]func(*testing.T) redis.UniversalClient{
		"BUSY":       refusal("-BUSY Valkey is busy running a script. You can only call SCRIPT KILL or SHUTDOWN NOSAVE.\r\n"),
		"maxclients": refusal("-ERR max number of clients reached\r\n"),
		"pool timeout": func(t *testing.T) redis.UniversalClient {
			c := redis.NewClient(&redis.Options{
				Addr: scriptedNode(t, func(args []string) string {
					if args[0] == "PING" {
						return "+PONG\r\n"
					}
					return "-ERR unknown command\r\n"
				}), Protocol: 2, DisableIdentity: true,
				MaxRetries: -1, PoolSize: 1, MaxActiveConns: 1, PoolTimeout: 20 * time.Millisecond,
			})
			t.Cleanup(func() { _ = c.Close() })
			held := c.Conn() // the pool's only connection, never returned
			require.NoError(t, held.Ping(context.Background()).Err())
			t.Cleanup(func() { _ = held.Close() })
			return c
		},
		"closed cluster node client": func(t *testing.T) redis.UniversalClient {
			c := fakeCluster(t, func([]string) string { return "*0\r\n" })
			// Load the slot map (and the node client) first, then close it.
			_, _, err := valkeystore.New(c, valkeystore.Options{}).Get(context.Background(), "t", docref.NewID())
			require.NoError(t, err)
			require.NoError(t, c.Close())
			return c
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, email := refConnectors(t, client(t))

			err := createDocument(store)
			require.ErrorIs(t, err, docref.ErrUnavailable)
			d := connectors.DecideRetry(registry.TypeStorage, err, "")
			assert.True(t, d.Retry, "storage: %v", err)
			assert.Equal(t, connectors.ClassTransient, d.Class)
			assert.Equal(t, "document-ref store unavailable", d.Reason)
			assert.Equal(t, "transient", d.Rule)

			out, err := sendWithAttachment(email)
			require.ErrorIs(t, err, docref.ErrUnavailable)
			assert.ErrorIs(t, err, connectors.ErrNotDelivered)
			assert.Equal(t, "not_delivered", out["deliveryOutcome"])
			d = connectors.DecideRetry(registry.TypeSendEmail, err, "")
			assert.True(t, d.Retry, "send-email: %v", err)
			assert.Equal(t, "transient, not delivered", d.Rule)
		})
	}
}

// A refused ASKING (the worker's ACL user lacks +asking) cannot succeed on a
// retry until the ACL is fixed: it is never reported as unavailable, and
// never retried automatically, by either connector. (Its class is not
// asserted: today nothing classifies a NOPERM reply, so it is unknown,
// "unclassified"; either unknown or permanent stops the retry loop.)
func TestRetryDecision_AskingRefusedNOPERM_NeverRetried(t *testing.T) {
	t.Parallel()
	const noperm = "-NOPERM User docref-worker has no permissions to run the 'asking' command\r\n"
	m := newMigratingPairWith(t, "*0\r\n", func(addr string) string { return addr }, noperm)
	store, email := refConnectors(t, m.client)

	err := createDocument(store)
	require.ErrorContains(t, err, "ASKING: NOPERM")
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
	d := connectors.DecideRetry(registry.TypeStorage, err, "")
	assert.False(t, d.Retry)
	assert.NotEqual(t, connectors.ClassTransient, d.Class)

	_, err = sendWithAttachment(email)
	require.ErrorContains(t, err, "ASKING: NOPERM")
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
	d = connectors.DecideRetry(registry.TypeSendEmail, err, "")
	assert.False(t, d.Retry)
	assert.NotEqual(t, connectors.ClassTransient, d.Class)
}

// A store built from invalid options (negative WaitReplicas) refuses every
// Put: a configuration error that no retry can fix, so a storage call that
// creates a ref is never retried. (The ref's best-effort cleanup delete
// still reaches Valkey; only Put is refused.)
func TestRetryDecision_InvalidOptions_NeverRetried(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, func(args []string) string {
		assert.NotEqual(t, "WAITAOF", args[0], "no write is attempted")
		if args[0] == "EVAL" {
			assert.NotContains(t, args[1], "PEXPIREAT", "the create script is never sent")
		}
		return "-ERR unexpected\r\n"
	})
	bucket := storage.NewMockStorageClient()
	require.NoError(t, bucket.Upload(context.Background(), "b", "k", []byte("x"), "text/plain"))
	all, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		DocRefs:       docref.NewService(valkeystore.New(c, valkeystore.Options{WaitReplicas: -1}), docref.NewMemoryContent("docs")),
		StorageProviders: map[string]storage.ProviderConstructor{
			"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return bucket, nil },
		},
	})
	require.NoError(t, err)

	err = createDocument(all[registry.TypeStorage])
	require.ErrorIs(t, err, valkeystore.ErrInvalidOptions)
	assert.NotErrorIs(t, err, docref.ErrUnavailable)
	assert.False(t, connectors.DecideRetry(registry.TypeStorage, err, "").Retry)
}
