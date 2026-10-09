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
