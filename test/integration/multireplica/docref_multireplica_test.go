//go:build integration

package connectors_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

// These tests run several "worker replicas" against one Valkey (reference
// metadata) and one S3 document bucket (content). Each replica is its own
// connectors.New with its own Valkey and S3 clients, the shape of separate
// processes. They share only Valkey, the document bucket and the tenant's
// object store (all external, shared state in production too).

func validConfig() connectors.Config {
	return connectors.Config{InternalToken: "test-token"}
}

// infra is the shared infrastructure every replica of one test uses.
type infra struct {
	valkey string
	bucket string
}

func sharedInfra(t *testing.T) infra {
	t.Helper()
	for _, env := range []string{"TEST_VALKEY_ADDR", "TEST_S3_ENDPOINT"} {
		if os.Getenv(env) == "" {
			if os.Getenv("CI") != "" {
				t.Fatalf("CI is set but %s is not: the multi-replica tests cannot be skipped in CI", env)
			}
			t.Skipf("%s not set (make test-integration)", env)
		}
	}
	bucket := "docs-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	_, err := newS3Client().CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	return infra{valkey: os.Getenv("TEST_VALKEY_ADDR"), bucket: bucket}
}

func newS3Client() *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(os.Getenv("TEST_S3_ENDPOINT"))
		o.UsePathStyle = true
	})
}

// replica is one worker process.
type replica struct {
	conns  map[string]connectors.Connector
	client *redis.Client
	store  *valkeystore.Store
	docs   *docref.Service
	emails *sendemail.MockSendEmailClient
}

// startReplica boots a worker: its own Valkey and S3 clients on the shared
// infrastructure, and the tenant's (mock) bucket.
func startReplica(t *testing.T, inf infra, bucket *storage.MockStorageClient) *replica {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: inf.valkey})
	require.NoError(t, client.Ping(context.Background()).Err())

	r := &replica{client: client, store: valkeystore.New(client, valkeystore.Options{TTL: time.Hour}), emails: sendemail.NewMockSendEmailClient()}
	r.docs = docref.NewService(r.store, s3content.New(newS3Client(), inf.bucket))
	cfg := validConfig()
	cfg.StorageProviders = map[string]storage.ProviderConstructor{
		"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return bucket, nil },
	}
	cfg.SendEmailProviders = map[string]sendemail.ProviderConstructor{
		"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return r.emails, nil },
	}
	cfg.DocRefs = r.docs
	var err error
	r.conns, err = connectors.New(cfg)
	require.NoError(t, err)
	return r
}

// stop terminates the worker: its connections close and its memory is gone.
func (r *replica) stop() { _ = r.client.Close() }

func (r *replica) fetchRef(t *testing.T, ctx context.Context, key string) string {
	t.Helper()
	out, err := r.conns["storage"].Execute(ctx, map[string]any{
		"operation": "fetch", "provider": "aws-s3", "bucket": "shared-bucket", "key": key, "createDocument": true,
	})
	require.NoError(t, err)
	return out["contentRef"].(string)
}

func (r *replica) sendWith(ctx context.Context, ref string) error {
	_, err := r.conns["send-email"].Execute(ctx, map[string]any{
		"provider": "sendgrid", "senderEmail": "a@example.com", "receiverEmail": "b@example.com",
		"body": "attached", "attachments": []any{ref},
	})
	return err
}

// attach has the replica email the document behind ref and returns the bytes sent.
func (r *replica) attach(t *testing.T, ctx context.Context, ref string) []byte {
	t.Helper()
	before := len(r.emails.Sent())
	require.NoError(t, r.sendWith(ctx, ref), "replica could not resolve %s", ref)
	sent := r.emails.Sent()
	require.Len(t, sent, before+1)
	return sent[len(sent)-1].Attachments[0].Content
}

func seededBucket(t *testing.T, docs map[string]string) *storage.MockStorageClient {
	t.Helper()
	bucket := storage.NewMockStorageClient()
	for key, body := range docs {
		require.NoError(t, bucket.Upload(context.Background(), "shared-bucket", key, []byte(body), "application/pdf"))
	}
	return bucket
}

func tenantCtx(t *testing.T) context.Context {
	return connectors.WithTenant(context.Background(), "tenant-"+t.Name())
}

// 1. Worker A writes, worker B reads.
func TestMultiReplica_WriteOnA_ReadOnB(t *testing.T) {
	inf := sharedInfra(t)
	bucket := seededBucket(t, map[string]string{"invoice.pdf": "invoice bytes"})
	a, b := startReplica(t, inf, bucket), startReplica(t, inf, bucket)
	defer a.stop()
	defer b.stop()
	ctx := tenantCtx(t)

	ref := a.fetchRef(t, ctx, "invoice.pdf")
	assert.Equal(t, "invoice bytes", string(b.attach(t, ctx, ref)), "no replica affinity: B resolves A's ref")
}

// 3. Process restart: the writer exits right after writing.
func TestMultiReplica_WriterProcessRestart(t *testing.T) {
	inf := sharedInfra(t)
	bucket := seededBucket(t, map[string]string{"contract.pdf": "contract bytes"})
	a := startReplica(t, inf, bucket)
	ctx := tenantCtx(t)

	ref := a.fetchRef(t, ctx, "contract.pdf")
	a.stop() // process gone

	restarted := startReplica(t, inf, bucket)
	defer restarted.stop()
	assert.Equal(t, "contract bytes", string(restarted.attach(t, ctx, ref)))
}

// 4. Rolling deployment: replicas are replaced one at a time while refs are in flight.
func TestMultiReplica_RollingDeployment(t *testing.T) {
	inf := sharedInfra(t)
	bucket := seededBucket(t, map[string]string{"one.pdf": "one", "two.pdf": "two"})
	a, b := startReplica(t, inf, bucket), startReplica(t, inf, bucket)
	ctx := tenantCtx(t)

	ref1 := a.fetchRef(t, ctx, "one.pdf")
	a.stop()
	a = startReplica(t, inf, bucket)
	ref2 := b.fetchRef(t, ctx, "two.pdf")
	b.stop()
	b = startReplica(t, inf, bucket)
	defer a.stop()
	defer b.stop()

	for _, r := range []*replica{a, b} {
		assert.Equal(t, "one", string(r.attach(t, ctx, ref1)))
		assert.Equal(t, "two", string(r.attach(t, ctx, ref2)))
	}
}

// 2. Multi-replica read/write: three replicas writing concurrently, every
// ref resolving on every replica.
func TestMultiReplica_ConcurrentReadWriteAcrossReplicas(t *testing.T) {
	inf := sharedInfra(t)
	docs := map[string]string{}
	for i := range 24 {
		docs[fmt.Sprintf("c-%d.pdf", i)] = fmt.Sprintf("concurrent-%d", i)
	}
	bucket := seededBucket(t, docs)
	replicas := []*replica{startReplica(t, inf, bucket), startReplica(t, inf, bucket), startReplica(t, inf, bucket)}
	defer func() {
		for _, r := range replicas {
			r.stop()
		}
	}()
	ctx := tenantCtx(t)

	var mu sync.Mutex
	refs := map[string]string{}
	var wg sync.WaitGroup
	i := 0
	for key, body := range docs {
		r := replicas[i%len(replicas)]
		i++
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref := r.fetchRef(t, ctx, key)
			mu.Lock()
			refs[ref] = body
			mu.Unlock()
		}()
	}
	wg.Wait()

	require.Len(t, refs, len(docs), "every write produced its own ref")
	for ref, body := range refs {
		for _, r := range replicas {
			assert.Equal(t, body, string(r.attach(t, ctx, ref)))
		}
	}
}

// 5. Concurrent updates: replicas racing to create the same ref ID — exactly
// one wins, and every replica sees the winner's bytes.
func TestMultiReplica_ConcurrentCreateOfOneID_OneWinner(t *testing.T) {
	inf := sharedInfra(t)
	bucket := seededBucket(t, nil)
	replicas := []*replica{startReplica(t, inf, bucket), startReplica(t, inf, bucket), startReplica(t, inf, bucket)}
	defer func() {
		for _, r := range replicas {
			r.stop()
		}
	}()
	ctx, tenant, id := context.Background(), "tenant-"+t.Name(), docref.NewID()

	start := make(chan struct{})
	var mu sync.Mutex
	winners := []string{}
	var wg sync.WaitGroup
	for w := range 12 {
		r := replicas[w%len(replicas)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			content := fmt.Sprintf("writer-%d", w)
			_, err := r.store.Put(ctx, docref.Ref{ID: id, TenantID: tenant, Bucket: inf.bucket, ObjectKey: content})
			if err == nil {
				mu.Lock()
				winners = append(winners, content)
				mu.Unlock()
				return
			}
			assert.ErrorIs(t, err, docref.ErrExists)
		}()
	}
	close(start)
	wg.Wait()

	require.Len(t, winners, 1, "a ref is create-only: exactly one writer wins")
	for _, r := range replicas {
		got, found, err := r.store.Get(ctx, tenant, id)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, winners[0], got.ObjectKey, "all replicas share one view")
	}
}

// 7. Delete and recreate, seen consistently from every replica.
func TestMultiReplica_DeleteAndRecreate(t *testing.T) {
	inf := sharedInfra(t)
	bucket := seededBucket(t, nil)
	a, b := startReplica(t, inf, bucket), startReplica(t, inf, bucket)
	defer a.stop()
	defer b.stop()
	ctx, tenant := context.Background(), "tenant-"+t.Name()

	ref, err := a.docs.Create(ctx, tenant, "text/plain", []byte("v1"))
	require.NoError(t, err)
	require.NoError(t, b.docs.Delete(ctx, tenant, ref.ID))

	_, _, err = a.docs.Read(ctx, tenant, ref.ID, 1<<20)
	assert.ErrorIs(t, err, docref.ErrNotFound, "a delete on B is visible on A at once")

	again, err := b.docs.Create(ctx, tenant, "text/plain", []byte("v2"))
	require.NoError(t, err, "the document can be created again")
	_, content, err := a.docs.Read(ctx, tenant, again.ID, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "v2", string(content))
}

// 8. Missing keys are handled explicitly, on every replica.
func TestMultiReplica_MissingRef(t *testing.T) {
	inf := sharedInfra(t)
	bucket := seededBucket(t, nil)
	r := startReplica(t, inf, bucket)
	defer r.stop()

	err := r.sendWith(tenantCtx(t), docref.NewID())
	assert.ErrorIs(t, err, connectors.ErrValidation, "an unknown ref is a validation error, not a crash or a silent empty attachment")
	assert.Empty(t, r.emails.Sent())
}

// A ref never crosses tenants, on any replica.
func TestMultiReplica_TenantIsolation(t *testing.T) {
	inf := sharedInfra(t)
	bucket := seededBucket(t, map[string]string{"private.pdf": "tenant a only"})
	a, b := startReplica(t, inf, bucket), startReplica(t, inf, bucket)
	defer a.stop()
	defer b.stop()

	ref := a.fetchRef(t, connectors.WithTenant(context.Background(), "tenant-a-"+t.Name()), "private.pdf")
	err := b.sendWith(connectors.WithTenant(context.Background(), "tenant-b-"+t.Name()), ref)
	assert.ErrorIs(t, err, connectors.ErrValidation, "another tenant's ref does not resolve")
	assert.Empty(t, b.emails.Sent())
}
