package s3content_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
)

type accessDenied struct{}

func (accessDenied) Error() string     { return "api error AccessDenied: Access Denied" }
func (accessDenied) ErrorCode() string { return "AccessDenied" }

// leastPrivilegeS3 behaves like a bucket whose policy grants the worker
// object access only under prefix: anything else is AccessDenied, as S3
// answers before it checks whether the key exists.
type leastPrivilegeS3 struct{ prefix string }

func (leastPrivilegeS3) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, nil
}
func (l leastPrivilegeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if !strings.HasPrefix(aws.ToString(in.Key), l.prefix) {
		return nil, accessDenied{}
	}
	return nil, &types.NoSuchKey{Message: aws.String("missing")}
}
func (leastPrivilegeS3) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
}
func (leastPrivilegeS3) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

// Under the runbook's least-privilege policy (object access on
// <bucket>/<prefix>* only) the startup check passes: its probe stays under
// the document prefix.
func TestCheck_PassesUnderPrefixScopedPolicy(t *testing.T) {
	t.Parallel()
	s := s3content.New(leastPrivilegeS3{prefix: "docrefs/"}, "docs")
	assert.NoError(t, s.Check(context.Background(), "docrefs/"))
	assert.Error(t, s.Check(context.Background(), ""), "a probe outside the granted prefix is refused, as S3 would")
}

// recordingS3 records the probe keys Check uses.
type recordingS3 struct {
	leastPrivilegeS3
	keys *[]string
}

func (r recordingS3) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	*r.keys = append(*r.keys, aws.ToString(in.Key))
	return r.leastPrivilegeS3.PutObject(ctx, in, opts...)
}

// Every check probes its own random key under the prefix, so replicas
// starting together never share (and race on) one probe object.
func TestCheck_ProbeKeysAreRandom(t *testing.T) {
	t.Parallel()
	var keys []string
	s := s3content.New(recordingS3{leastPrivilegeS3{prefix: "docrefs/"}, &keys}, "docs")
	for range 50 {
		assert.NoError(t, s.Check(context.Background(), "docrefs/"))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		assert.True(t, strings.HasPrefix(k, "docrefs/.docref-check/"), k)
		assert.False(t, seen[k], "duplicate probe key %s", k)
		seen[k] = true
	}
	assert.Len(t, seen, 50)
}
