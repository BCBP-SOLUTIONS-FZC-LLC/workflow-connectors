package storage_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/storage"
)

// A bucket or key no provider accepts, or unsafe to store and log, is invalid
// input: refused before any provider client is built or called.
func TestStorage_BucketAndKey_AreValidated(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ field, value string }{
		"key over 1024 bytes":    {"key", strings.Repeat("k", 1025)},
		"bucket over 1024 bytes": {"bucket", strings.Repeat("b", 1025)},
		"key not UTF-8":          {"key", "a\xffb"},
		"bucket not UTF-8":       {"bucket", "\xc3"},
		"key with NUL":           {"key", "a\x00b"},
		"key with newline":       {"key", "a\nb"},
		"key with DEL":           {"key", "a\x7fb"},
		"key with C1 control":    {"key", "a\u0085b"},
		"bucket with tab":        {"bucket", "b\tx"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			built := false
			conn := storage.New(map[string]storage.ProviderConstructor{
				"aws-s3": func(context.Context, map[string]any) (storage.ProviderClient, error) {
					built = true
					return storage.NewMockStorageClient(), nil
				},
			}, nil)
			for _, op := range []string{"fetch", "upload", "delete"} {
				_, err := conn.Execute(tctx, with(base(op), "content", "x", tc.field, tc.value))
				require.ErrorIs(t, err, shared.ErrValidation, op)
				assert.NotErrorIs(t, err, shared.ErrUpstream, op)
				assert.Contains(t, err.Error(), tc.field, op)
			}
			assert.False(t, built, "no provider client is built for an invalid name")
		})
	}
}

func TestStorage_BucketAndKey_AtTheLimitAndUnicode_AreAccepted(t *testing.T) {
	t.Parallel()

	for _, key := range []string{strings.Repeat("k", 1024), "reports/2026/café – Q3 (final).pdf"} {
		client := storage.NewMockStorageClient()
		conn := storage.New(mockStorageProviders(client), nil)
		_, err := conn.Execute(tctx, with(base("upload"), "content", "x", "key", key))
		require.NoError(t, err)
		got, _, err := client.Fetch(context.Background(), "b", key, 10)
		require.NoError(t, err)
		assert.Equal(t, "x", string(got))
	}
}

// Base64 content is sized before it is decoded: content decoding to more than
// the object limit is refused without a second buffer of its size (even when
// it is not valid base64 at all), while
// content decoding to exactly the limit — padded or not, wrapped in lines or
// not — is accepted.
func TestStorage_UploadBase64_SizedBeforeDecoding(t *testing.T) {
	t.Parallel()

	exact := make([]byte, shared.MaxObjectBytes) // 52428800 % 3 == 2: one '=' of padding
	exactB64 := base64.StdEncoding.EncodeToString(exact)
	require.True(t, strings.HasSuffix(exactB64, "=") && !strings.HasSuffix(exactB64, "=="))
	over := base64.StdEncoding.EncodeToString(make([]byte, shared.MaxObjectBytes+1)) // no padding

	var wrapped strings.Builder
	for i := 0; i < len(exactB64); i += 76 {
		end := min(i+76, len(exactB64))
		wrapped.WriteString(exactB64[i:end])
		wrapped.WriteString("\r\n")
	}

	for name, tc := range map[string]struct {
		content string
		ok      bool
	}{
		"exactly the limit":         {exactB64, true},
		"exactly the limit wrapped": {wrapped.String(), true},
		"one byte over":             {over, false},
		"one byte, double padding":  {"QQ==", true},
		"one byte, padding split":   {"QQ=\r\n=", true},
		// Invalid as well as oversized: the size error, not the decoder's,
		// proves the size is checked before anything is decoded.
		"oversized and invalid": {over[:len(over)-1] + "*", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := storage.NewMockStorageClient()
			conn := storage.New(mockStorageProviders(client), nil)
			out, err := conn.Execute(tctx, with(base("upload"), "content", tc.content, "contentEncoding", "base64"))
			if !tc.ok {
				require.ErrorIs(t, err, shared.ErrValidation)
				assert.Contains(t, err.Error(), "exceeds the")
				return
			}
			require.NoError(t, err)
			assert.Positive(t, out["sizeBytes"])
		})
	}
}
