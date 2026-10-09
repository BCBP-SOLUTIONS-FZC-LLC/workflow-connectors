package s3content_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref/s3content"
)

// scriptedS3 returns the configured result for each call.
type scriptedS3 struct {
	putErr, deleteErr error
	getOut            *s3.GetObjectOutput
	getErr            error
	putIn             *s3.PutObjectInput
}

func (f *scriptedS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putIn = in
	return &s3.PutObjectOutput{}, f.putErr
}
func (f *scriptedS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f.getOut, f.getErr
}
func (f *scriptedS3) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, f.deleteErr
}
func (f *scriptedS3) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error { c.closed = true; return nil }

// An existing object at the probe key still proves the bucket is readable;
// its body is closed.
func TestCheck_ProbeObjectExists_Passes(t *testing.T) {
	t.Parallel()
	body := &closeTracker{Reader: strings.NewReader("x")}
	s := s3content.New(&scriptedS3{getOut: &s3.GetObjectOutput{Body: body}}, "docs")
	require.NoError(t, s.Check(context.Background(), "docrefs/"))
	assert.True(t, body.closed)
}

func TestPut_Failure_IsWrapped(t *testing.T) {
	t.Parallel()
	cause := errors.New("throttled")
	api := &scriptedS3{putErr: cause}
	err := s3content.New(api, "docs").Put(context.Background(), "t/a", []byte("x"), "")
	require.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "s3 put s3://docs/t/a")
	assert.Nil(t, api.putIn.ContentType, "no content type is sent when none is given")
}

func TestDelete_Failure(t *testing.T) {
	t.Parallel()
	cause := accessDenied{}
	err := s3content.New(&scriptedS3{deleteErr: cause}, "docs").Delete(context.Background(), "docs", "t/a")
	require.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "s3 delete s3://docs/t/a")

	missing := &scriptedS3{deleteErr: &types.NoSuchKey{Message: aws.String("gone")}}
	assert.NoError(t, s3content.New(missing, "docs").Delete(context.Background(), "docs", "t/a"), "a missing object is already deleted")
}
