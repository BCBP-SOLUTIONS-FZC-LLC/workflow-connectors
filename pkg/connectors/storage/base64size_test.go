package storage

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// base64DecodedSize is exact for valid base64, wherever the line breaks are,
// including between the two '=' of the padding.
func TestBase64DecodedSize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int64{
		"":              0,
		"QQ==":          1,
		"QQ=\n=":        1,
		"QQ=\r\n=\r\n":  1,
		"QUI=":          2,
		"QUI=\r\n":      2,
		"QUJD":          3,
		"QU\nJD\n":      3,
		"QUJDRA==\n":    4,
		"==":            0, // invalid: never negative; the decoder refuses it
		"\n\r":          0,
		"QUJD\n=\n=\n=": 1, // invalid: at most two '=' count as padding
	} {
		assert.Equal(t, want, base64DecodedSize(in), "%q", in)
		if decoded, err := base64.StdEncoding.DecodeString(in); err == nil {
			assert.Equal(t, int64(len(decoded)), want, "%q decodes to a different size", in)
		}
	}
}

// For every length (each padding case) and line breaks inserted anywhere —
// MIME's 76-column CRLF wrapping, a lone LF, breaks inside the padding —
// base64DecodedSize equals what the decoder yields. An underestimate would let
// an oversized upload past checkUploadSize; an overestimate would refuse a
// valid one at the limit.
func TestBase64DecodedSize_MatchesTheDecoderWithLineBreaksAnywhere(t *testing.T) {
	t.Parallel()
	wrap := func(s, sep string, every int) string {
		var b strings.Builder
		for i := 0; i < len(s); i += every {
			b.WriteString(s[i:min(i+every, len(s))])
			b.WriteString(sep)
		}
		return b.String()
	}
	for n := range 200 {
		enc := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xA5}, n))
		variants := []string{enc, wrap(enc, "\r\n", 76), wrap(enc, "\n", 1), wrap(enc, "\r", 3), "\n" + enc + "\r\n\r\n"}
		for i := range len(enc) + 1 {
			variants = append(variants, enc[:i]+"\r\n"+enc[i:])
		}
		for _, in := range variants {
			decoded, err := base64.StdEncoding.DecodeString(in)
			if !assert.NoError(t, err, "%q", in) {
				continue
			}
			assert.Equal(t, int64(len(decoded)), base64DecodedSize(in), "n=%d %q", n, in)
		}
	}
}
