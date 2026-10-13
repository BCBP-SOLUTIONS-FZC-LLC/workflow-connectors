// Package s3content is the S3 content store for document references: the
// system of record for document content. It uses the worker's own S3 client
// (the platform's credential model: the worker's IAM role or credential
// chain) — never a tenant's credentials, and a reference never carries any.
//
// The bucket should have a lifecycle rule expiring objects under the
// document prefix after the reference TTL plus a margin (2 days for the
// default 24 h), so an object outlives its reference but never accumulates.
// See docs/runbooks/document-refs.md.
package s3content

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/docref"
)

// API is the subset of *s3.Client the store uses.
type API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

type Store struct {
	client API
	bucket string
}

// New returns a content store writing to bucket through client.
func New(client API, bucket string) *Store { return &Store{client: client, bucket: bucket} }

func (s *Store) Bucket() string { return s.bucket }

// Check verifies, at startup, that the bucket is reachable with the
// worker's credentials and that a missing object reads as NoSuchKey. Without
// s3:ListBucket S3 answers 403 for a missing object, which would make a
// deleted document look like a transient access failure, retried forever.
//
// It then writes and deletes an empty object at the probe key, so missing
// s3:PutObject or s3:DeleteObject (which Create and Delete need) fails at
// startup rather than on the first document. A probe object left behind by a
// failed delete is removed by the bucket lifecycle rule.
//
// keyPrefix is the document key prefix the service writes under (the value
// given to docref.WithKeyPrefix): the probe uses a random key beneath it, so
// it needs no permission beyond the documented <bucket>/<prefix>* grant.
func (s *Store) Check(ctx context.Context, keyPrefix string) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("docref: document bucket %q unreachable: %w", s.bucket, err)
	}
	// A random key, so concurrent checks (replicas starting together) never
	// share a probe object.
	probe := keyPrefix + ".docref-check/" + rand.Text()
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(probe)})
	switch {
	case err == nil:
		_ = out.Body.Close()
	case !isNotFound(err):
		return fmt.Errorf("docref: a missing object in %q does not read as NoSuchKey (grant s3:ListBucket on the bucket): %w", s.bucket, err)
	}
	if err := s.Put(ctx, probe, nil, ""); err != nil {
		return fmt.Errorf("docref: cannot write documents under %q in %q (grant s3:PutObject): %w", keyPrefix, s.bucket, err)
	}
	if err := s.Delete(ctx, s.bucket, probe); err != nil {
		return fmt.Errorf("docref: cannot delete documents under %q in %q (grant s3:DeleteObject): %w", keyPrefix, s.bucket, err)
	}
	return nil
}

func (s *Store) Put(ctx context.Context, key string, content []byte, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(content),
		ContentLength: aws.Int64(int64(len(content))),
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("s3 put s3://%s/%s: %w", s.bucket, key, err)
	}
	return nil
}

// Open streams the object; the body is never buffered here.
func (s *Store) Open(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if isNotFound(err) {
		return nil, 0, docref.ErrObjectNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("s3 get s3://%s/%s: %w", bucket, key, err)
	}
	size := int64(-1)
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return out.Body, size, nil
}

func (s *Store) Delete(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3 delete s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}

// isNotFound reports a missing object — NoSuchKey only. Any other 404 (a
// missing bucket in particular) is an infrastructure fault, not a missing
// document, and must not read as one.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}
	var coded interface{ ErrorCode() string }
	return errors.As(err, &coded) && coded.ErrorCode() == "NoSuchKey"
}

var _ docref.ContentStore = (*Store)(nil)
