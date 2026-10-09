//go:build integration

// Package integration_test runs whole workflow scenarios — a storage task
// on one worker replica, a send-email task on another — against the real
// infrastructure from docker-compose.yml: document content in S3 (floci),
// reference metadata in Valkey, send intents in PostgreSQL through PgBouncer.
// Only the tenant's providers (their bucket, their email service) are
// scripted, so each scenario controls exactly what the provider returns.
//
// Runs with `make test-integration` (which starts docker-compose.yml).
package integration_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/migrate"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/v2/pkg/pgcommon"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/valkeystore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent"
	intentsql "github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendintent/sqlstore"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("CI is set but %s is not: these tests cannot be skipped in CI", name)
		}
		t.Skipf("%s not set (make test-integration)", name)
	}
	return v
}

func s3Client(t *testing.T) *s3.Client {
	t.Helper()
	endpoint := env(t, "TEST_S3_ENDPOINT")
	return s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

// platform is the shared infrastructure of one scenario: a fresh document
// bucket, the shared Valkey and the send-intent store behind PgBouncer.
type platform struct {
	valkeyAddr string
	bucket     string
	refTTL     time.Duration
	intents    *intentsql.Store
}

var (
	schemaOnce sync.Once
	schemaErr  error
)

func newPlatform(t *testing.T) *platform {
	t.Helper()
	direct, bouncer := env(t, "TEST_POSTGRES_DSN"), env(t, "TEST_PGBOUNCER_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	schemaOnce.Do(func() { schemaErr = intentsql.ApplySchema(ctx, &migrate.Runner{DSN: direct}) })
	require.NoError(t, schemaErr)
	pool, err := pgcommon.NewPool(ctx, pgcommon.Config{DSN: bouncer, MaxConns: 10, PoolName: "integration", PGBouncerMode: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	bucket := "docs-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	_, err = s3Client(t).CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	return &platform{valkeyAddr: env(t, "TEST_VALKEY_ADDR"), bucket: bucket, refTTL: time.Hour, intents: intentsql.New(pool)}
}

// replica is one worker process: its own Valkey and S3 clients and its own
// connectors.New, sharing only the platform and the tenant's providers.
type replica struct {
	byType  map[string]connectors.Connector
	docRefs *docref.Service
}

func (p *platform) startReplica(t *testing.T, tenantBucket storage.ProviderClient, email sendemail.ProviderClient) *replica {
	t.Helper()
	vk := redis.NewClient(&redis.Options{Addr: p.valkeyAddr})
	t.Cleanup(func() { _ = vk.Close() })
	ctx := context.Background()
	content := s3content.New(s3Client(t), p.bucket)
	require.NoError(t, valkeystore.CheckDurability(ctx, vk), "the compose Valkey runs the production persistence settings")
	require.NoError(t, content.Check(ctx, "docrefs/"))

	r := &replica{docRefs: docref.NewService(valkeystore.New(vk, valkeystore.Options{TTL: p.refTTL}), content, docref.WithKeyPrefix("docrefs/"))}
	byType, err := connectors.New(connectors.Config{
		InternalToken: "test-token",
		StorageProviders: map[string]storage.ProviderConstructor{
			"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) { return tenantBucket, nil },
		},
		SendEmailProviders: map[string]sendemail.ProviderConstructor{
			"sendgrid": func(context.Context, map[string]any) (sendemail.ProviderClient, error) { return email, nil },
		},
		DocRefs:     r.docRefs,
		SendIntents: p.intents,
	})
	require.NoError(t, err)
	r.byType = byType
	return r
}

// attempt is one Execute the simulated worker made, and its decision.
type attempt struct {
	out      map[string]any
	err      error
	decision connectors.RetryDecision
}

// runTask is the worker's retry loop: re-run only while DecideRetry allows,
// up to maxAttempts.
func runTask(ctx context.Context, conn connectors.Connector, method string, input map[string]any, maxAttempts int) []attempt {
	var attempts []attempt
	for range maxAttempts {
		out, err := conn.Execute(ctx, input)
		a := attempt{out: out, err: err}
		if err != nil {
			a.decision = connectors.DecideRetry(conn.Type(), err, method)
		}
		attempts = append(attempts, a)
		if err == nil || !a.decision.Retry {
			break
		}
	}
	return attempts
}

func last(attempts []attempt) attempt { return attempts[len(attempts)-1] }

// emailStep is one scripted provider response.
type emailStep struct {
	err       error
	delivered bool // the provider accepted the message, whatever the caller saw
}

// scriptedEmail plays steps in order (the last repeats) and records every
// message the provider actually accepted.
type scriptedEmail struct {
	mu        sync.Mutex
	steps     []emailStep
	calls     int
	delivered []sendemail.EmailMessage
}

func (s *scriptedEmail) Send(_ context.Context, msg sendemail.EmailMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step := s.steps[min(s.calls, len(s.steps)-1)]
	s.calls++
	if step.err == nil || step.delivered {
		s.delivered = append(s.delivered, msg)
	}
	if step.err != nil {
		return "", step.err
	}
	return "provider-msg-1", nil
}

func (s *scriptedEmail) sent() []sendemail.EmailMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sendemail.EmailMessage(nil), s.delivered...)
}

func tenantBucketWith(t *testing.T, key string, body []byte) *storage.MockStorageClient {
	t.Helper()
	b := storage.NewMockStorageClient()
	require.NoError(t, b.Upload(context.Background(), "tenant-bucket", key, body, "application/pdf"))
	return b
}

func fetchInput(key string) map[string]any {
	return map[string]any{"operation": "fetch", "provider": "aws-s3", "bucket": "tenant-bucket", "key": key, "createDocument": true}
}

func emailInput(extra ...any) map[string]any {
	in := map[string]any{"provider": "sendgrid", "senderEmail": "billing@example.com", "receiverEmail": "customer@example.com", "body": "Your invoice"}
	for i := 0; i+1 < len(extra); i += 2 {
		in[extra[i].(string)] = extra[i+1]
	}
	return in
}

func tenantCtx(tenant string) context.Context {
	return connectors.WithTenant(context.Background(), tenant)
}

func intent(t *testing.T, p *platform, tenant, key string) sendintent.Intent {
	t.Helper()
	in, found, err := p.intents.Get(context.Background(), tenant, key)
	require.NoError(t, err)
	require.True(t, found)
	return in
}

func overwriteObject(t *testing.T, bucket, key string, body []byte) {
	t.Helper()
	_, err := s3Client(t).PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	require.NoError(t, err)
}

func deleteObject(t *testing.T, bucket, key string) {
	t.Helper()
	_, err := s3Client(t).DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.NoError(t, err)
}
