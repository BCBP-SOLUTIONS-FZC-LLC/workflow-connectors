//go:build integration

package valkeystore_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
)

// sharedClient connects to TEST_VALKEY_ADDR (CI and `make test-integration`
// start a Valkey with appendonly yes, appendfsync always, noeviction).
func sharedClient(t *testing.T) *redis.Client {
	t.Helper()
	if os.Getenv("CI") != "" && os.Getenv("TEST_VALKEY_ADDR") == "" {
		t.Fatal("CI is set but TEST_VALKEY_ADDR is not: the Valkey tests cannot be skipped in CI")
	}
	addr := os.Getenv("TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("TEST_VALKEY_ADDR not set (make test-integration)")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, c.Ping(context.Background()).Err())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func tenant() string { return "tenant-" + uuid.NewString() }

// meta is a ref's metadata as the service would write it.
func meta(tn string) docref.Ref {
	id := docref.NewID()
	return docref.Ref{ID: id, TenantID: tn, Bucket: "docs", ObjectKey: tn + "/" + id[len(docref.Prefix):],
		ContentType: "application/pdf", Size: 7, SHA256: docref.Checksum([]byte("%PDF-1."))}
}

func TestPutGet_RoundTrip(t *testing.T) {
	t.Parallel()
	c := sharedClient(t)
	s, ctx, tn := valkeystore.New(c, valkeystore.Options{TTL: time.Hour}), context.Background(), tenant()

	want := meta(tn)
	put, err := s.Put(ctx, want)
	require.NoError(t, err)
	assert.False(t, put.CreatedAt.IsZero())
	assert.Equal(t, put.CreatedAt, put.UpdatedAt)

	got, found, err := s.Get(ctx, tn, put.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, put, got)

	ttl, err := c.PTTL(ctx, valkeystore.Key(put.ID)).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, 59*time.Minute, "Valkey expires the key itself")
}

// Valkey holds metadata only: exactly the documented fields, no content.
func TestPut_StoresMetadataOnly(t *testing.T) {
	t.Parallel()
	c := sharedClient(t)
	s, ctx := valkeystore.New(c, valkeystore.Options{}), context.Background()
	ref, err := s.Put(ctx, meta(tenant()))
	require.NoError(t, err)

	keys, err := c.HKeys(ctx, valkeystore.Key(ref.ID)).Result()
	require.NoError(t, err)
	assert.ElementsMatch(t, valkeystore.Fields, keys)
	assert.NotContains(t, keys, "content")

	usage, err := c.MemoryUsage(ctx, valkeystore.Key(ref.ID)).Result()
	require.NoError(t, err)
	assert.Less(t, usage, int64(1024), "a ref costs a few hundred bytes whatever the document size")
}

func TestKey_Format(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "docref:3f2a", valkeystore.Key("docref:3f2a"))
	assert.Equal(t, "docref:3f2a", valkeystore.Key("3f2a"))
}

func TestPut_IsCreateOnly(t *testing.T) {
	t.Parallel()
	s, ctx, tn := valkeystore.New(sharedClient(t), valkeystore.Options{}), context.Background(), tenant()
	first, err := s.Put(ctx, meta(tn))
	require.NoError(t, err)

	second := meta(tn)
	second.ID, second.ObjectKey = first.ID, "elsewhere"
	_, err = s.Put(ctx, second)
	assert.ErrorIs(t, err, docref.ErrExists)
	got, _, err := s.Get(ctx, tn, first.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ObjectKey, got.ObjectKey, "an existing ref is never overwritten")
}

func TestPut_ConcurrentSameID_ExactlyOneWins(t *testing.T) {
	t.Parallel()
	s, ctx, tn, id := valkeystore.New(sharedClient(t), valkeystore.Options{}), context.Background(), tenant(), docref.NewID()

	start := make(chan struct{})
	var mu sync.Mutex
	wins := 0
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			<-start
			ref := meta(tn)
			ref.ID, ref.ObjectKey = id, fmt.Sprint(i)
			if _, err := s.Put(ctx, ref); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else {
				assert.ErrorIs(t, err, docref.ErrExists)
			}
		})
	}
	close(start)
	wg.Wait()
	assert.Equal(t, 1, wins)
}

func TestGet_MissingKey(t *testing.T) {
	t.Parallel()
	s := valkeystore.New(sharedClient(t), valkeystore.Options{})
	ref, found, err := s.Get(context.Background(), tenant(), docref.NewID())
	require.NoError(t, err, "a missing key is not an error")
	assert.False(t, found)
	assert.Equal(t, docref.Ref{}, ref)
}

func TestDelete_ThenRecreate(t *testing.T) {
	t.Parallel()
	s, ctx, tn := valkeystore.New(sharedClient(t), valkeystore.Options{}), context.Background(), tenant()
	ref, err := s.Put(ctx, meta(tn))
	require.NoError(t, err)

	require.NoError(t, s.Delete(ctx, tn, ref.ID))
	require.NoError(t, s.Delete(ctx, tn, ref.ID), "deleting a missing ref is not an error")
	_, found, err := s.Get(ctx, tn, ref.ID)
	require.NoError(t, err)
	assert.False(t, found)

	again := meta(tn)
	again.ID = ref.ID
	_, err = s.Put(ctx, again)
	require.NoError(t, err)
	_, found, err = s.Get(ctx, tn, ref.ID)
	require.NoError(t, err)
	assert.True(t, found)
}

func TestTenantIsolation(t *testing.T) {
	t.Parallel()
	s, ctx, owner := valkeystore.New(sharedClient(t), valkeystore.Options{}), context.Background(), tenant()
	ref, err := s.Put(ctx, meta(owner))
	require.NoError(t, err)

	_, found, err := s.Get(ctx, tenant(), ref.ID)
	require.NoError(t, err)
	assert.False(t, found, "another tenant never resolves the ref")

	require.NoError(t, s.Delete(ctx, tenant(), ref.ID))
	_, found, err = s.Get(ctx, owner, ref.ID)
	require.NoError(t, err)
	assert.True(t, found, "another tenant cannot delete the ref")
}

func TestGet_ExpiresWithTTL(t *testing.T) {
	t.Parallel()
	s, ctx, tn := valkeystore.New(sharedClient(t), valkeystore.Options{TTL: 150 * time.Millisecond}), context.Background(), tenant()
	ref, err := s.Put(ctx, meta(tn))
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)
	_, found, err := s.Get(ctx, tn, ref.ID)
	require.NoError(t, err)
	assert.False(t, found, "Valkey's key expiry retires the ref; no prune job exists")
}

func TestCheckDurability_ConfiguredServerPasses(t *testing.T) {
	t.Parallel()
	assert.NoError(t, valkeystore.CheckDurability(context.Background(), sharedClient(t)))
}

// ---- Dedicated Valkey containers (TEST_VALKEY_DOCKER=1) --------------------
// Restarting a server must not disturb other test packages, so these tests
// run their own containers.

// valkeyImage is the image docker-compose.yml runs, pinned by the same digest
// and pulled from the same registry (WC_REGISTRY, default docker.io; CI uses
// mirror.gcr.io to avoid Docker Hub's anonymous pull rate limit).
func valkeyImage() string {
	registry := os.Getenv("WC_REGISTRY")
	if registry == "" {
		registry = "docker.io"
	}
	return registry + "/valkey/valkey:8-alpine@sha256:081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507"
}

func dockerValkey(t *testing.T, args ...string) (name, addr string) {
	t.Helper()
	if os.Getenv("TEST_VALKEY_DOCKER") == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI is set but TEST_VALKEY_DOCKER is not: the Valkey restart test cannot be skipped in CI")
		}
		t.Skip("TEST_VALKEY_DOCKER not set (make test-integration)")
	}
	name = "docref-valkey-" + uuid.NewString()[:8]
	run := append([]string{"run", "-d", "--name", name, "-p", "127.0.0.1::6379", valkeyImage(), "valkey-server"}, args...)
	out, err := exec.Command("docker", run...).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	port, err := exec.Command("docker", "port", name, "6379/tcp").Output()
	require.NoError(t, err)
	addr = strings.TrimSpace(strings.Split(string(port), "\n")[0])
	waitReady(t, addr)
	return name, addr
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = c.Close() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := c.Ping(context.Background()).Err(); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("valkey at %s not ready: %v", addr, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// 6. Valkey restart and recovery from its configured persistence.
func TestValkeyRestart_RefsRecoverFromAOF(t *testing.T) {
	name, addr := dockerValkey(t, "--appendonly", "yes", "--appendfsync", "always", "--maxmemory-policy", "noeviction", "--save", "")
	ctx, tn := context.Background(), tenant()

	writer := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, valkeystore.CheckDurability(ctx, writer))
	s := valkeystore.New(writer, valkeystore.Options{TTL: time.Hour})
	refs := map[string]docref.Ref{}
	for range 5 {
		ref, err := s.Put(ctx, meta(tn))
		require.NoError(t, err)
		refs[ref.ID] = ref
	}
	_ = writer.Close()

	// Restart the server. RDB snapshots are off (--save ""), so only the AOF
	// can bring the refs back.
	out, err := exec.Command("docker", "restart", name).CombinedOutput()
	require.NoError(t, err, string(out))
	port, err := exec.Command("docker", "port", name, "6379/tcp").Output()
	require.NoError(t, err)
	addr = strings.TrimSpace(strings.Split(string(port), "\n")[0])
	waitReady(t, addr)

	for _, label := range []string{"replica A", "replica B"} {
		reader := redis.NewClient(&redis.Options{Addr: addr})
		rs := valkeystore.New(reader, valkeystore.Options{})
		for id, want := range refs {
			got, found, err := rs.Get(ctx, tn, id)
			require.NoError(t, err)
			require.Truef(t, found, "%s: ref %s lost across a Valkey restart", label, id)
			assert.Equal(t, want, got)
		}
		ttl, err := reader.PTTL(ctx, valkeystore.Key(firstKey(refs))).Result()
		require.NoError(t, err)
		assert.Positive(t, ttl, "the expiry survives the restart")
		_ = reader.Close()
	}
}

// Without AOF the store refuses to report a write as durable, and the
// startup check refuses the server.
func TestNonDurableServer_IsRefused(t *testing.T) {
	_, addr := dockerValkey(t, "--appendonly", "no", "--maxmemory-policy", "allkeys-lru")
	ctx := context.Background()
	c := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = c.Close() }()

	var derr *valkeystore.DurabilityError
	require.ErrorAs(t, valkeystore.CheckDurability(ctx, c), &derr)
	assert.Len(t, derr.Problems, 3, "WAITAOF refused, no AOF, and an eviction policy")

	_, err := valkeystore.New(c, valkeystore.Options{WaitAOFTimeout: 300 * time.Millisecond}).Put(ctx, meta(tenant()))
	assert.ErrorIs(t, err, valkeystore.ErrNotPersisted, "a write that cannot reach an AOF is never reported as stored")
}

// A managed Valkey that blocks CONFIG cannot hide a missing append-only
// file: the WAITAOF probe refuses it, rather than letting the worker start
// and fail every Put.
func TestDurability_ConfigBlocked_AOFOff_IsRefused(t *testing.T) {
	_, addr := dockerValkey(t, "--appendonly", "no", "--rename-command", "CONFIG", "")
	c := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = c.Close() }()

	err := valkeystore.CheckDurability(context.Background(), c)
	var derr *valkeystore.DurabilityError
	require.ErrorAs(t, err, &derr)
	assert.NotErrorIs(t, err, valkeystore.ErrConfigUnavailable)
}

// The same managed setup with AOF on passes the probe and reports only that
// the settings must be checked in the provider's console.
func TestDurability_ConfigBlocked_AOFOn_IsConfigUnavailable(t *testing.T) {
	_, addr := dockerValkey(t, "--appendonly", "yes", "--rename-command", "CONFIG", "")
	c := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = c.Close() }()

	assert.ErrorIs(t, valkeystore.CheckDurability(context.Background(), c), valkeystore.ErrConfigUnavailable)
}

// Many concurrent Puts through a small connection pool: each write is
// confirmed by a WAITAOF on its own connection, and every ref is stored.
func TestPut_ConcurrentWritesOnSmallPool_EachConfirmed(t *testing.T) {
	t.Parallel()
	addr := os.Getenv("TEST_VALKEY_ADDR")
	if addr == "" {
		sharedClient(t) // skips, or fails in CI
	}
	c := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 3})
	defer func() { _ = c.Close() }()
	s, ctx, tn := valkeystore.New(c, valkeystore.Options{}), context.Background(), tenant()

	var wg sync.WaitGroup
	ids := make([]string, 60)
	for i := range ids {
		wg.Go(func() {
			ref, err := s.Put(ctx, meta(tn))
			if assert.NoError(t, err) {
				ids[i] = ref.ID
			}
		})
	}
	wg.Wait()
	for _, id := range ids {
		_, found, err := s.Get(ctx, tn, id)
		require.NoError(t, err)
		assert.True(t, found, id)
	}
}

// 8. A Valkey restart does not affect document retrieval: the metadata is
// replayed from the AOF and the content is still read from S3, verified.
func TestValkeyRestart_DocumentsStillResolveFromS3(t *testing.T) {
	s3c, bucket := s3Bucket(t)
	name, addr := dockerValkey(t, "--appendonly", "yes", "--appendfsync", "always", "--maxmemory-policy", "noeviction", "--save", "")
	ctx, tn := context.Background(), tenant()

	writer := redis.NewClient(&redis.Options{Addr: addr})
	svc := docref.NewService(valkeystore.New(writer, valkeystore.Options{}), s3content.New(s3c, bucket))
	docs := map[string]string{}
	for i := range 3 {
		ref, err := svc.Create(ctx, tn, "text/plain", []byte(fmt.Sprintf("document %d", i)))
		require.NoError(t, err)
		docs[ref.ID] = fmt.Sprintf("document %d", i)
	}
	_ = writer.Close()

	out, err := exec.Command("docker", "restart", name).CombinedOutput()
	require.NoError(t, err, string(out))
	port, err := exec.Command("docker", "port", name, "6379/tcp").Output()
	require.NoError(t, err)
	addr = strings.TrimSpace(strings.Split(string(port), "\n")[0])
	waitReady(t, addr)

	reader := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = reader.Close() }()
	after := docref.NewService(valkeystore.New(reader, valkeystore.Options{}), s3content.New(s3c, bucket))
	for id, body := range docs {
		_, content, err := after.Read(ctx, tn, id, 1<<20)
		require.NoError(t, err)
		assert.Equal(t, body, string(content))
	}
}

func s3Bucket(t *testing.T) (*s3.Client, string) {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI is set but TEST_S3_ENDPOINT is not: the S3 tests cannot be skipped in CI")
		}
		t.Skip("TEST_S3_ENDPOINT not set (make test-integration)")
	}
	client := s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	bucket := "docs-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	_, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	return client, bucket
}

func firstKey(m map[string]docref.Ref) string {
	for k := range m {
		return k
	}
	return ""
}

// A hash this store could not have written is an integrity violation, never
// a transient error to retry.
func TestGet_CorruptHash_IsIntegrityViolation(t *testing.T) {
	t.Parallel()
	c := sharedClient(t)
	s, ctx, tn := valkeystore.New(c, valkeystore.Options{}), context.Background(), tenant()
	ref, err := s.Put(ctx, meta(tn))
	require.NoError(t, err)
	require.NoError(t, c.HSet(ctx, valkeystore.Key(ref.ID), "size", "not-a-number").Err())

	_, _, err = s.Get(ctx, tn, ref.ID)
	assert.ErrorIs(t, err, docref.ErrIntegrityViolation)
}
