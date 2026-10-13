package sqlstore

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestErrorText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "drive: 503", errorText("drive: 503"))
	assert.Equal(t, "ab", errorText("a\x00b"), "NUL removed")
	assert.Equal(t, "a�b", errorText("a\xffb"), "invalid UTF-8 replaced")
	assert.Equal(t, strings.Repeat("x", maxErrorBytes), errorText(strings.Repeat("x", maxErrorBytes)), "the limit itself is kept")

	long := errorText(strings.Repeat("x", 10*maxErrorBytes))
	assert.Len(t, long, maxErrorBytes)
	assert.True(t, strings.HasSuffix(long, "…"))

	// A cut never splits a rune: 'é' is two bytes, so the byte budget
	// before the ellipsis (1021) falls inside one.
	multi := errorText(strings.Repeat("é", maxErrorBytes))
	assert.True(t, utf8.ValidString(multi))
	assert.LessOrEqual(t, len(multi), maxErrorBytes)
	assert.True(t, strings.HasSuffix(multi, "é…"))

	body := errorText("googleapi: 500 body: \x00" + strings.Repeat("\xfe<html>", 2000))
	assert.True(t, utf8.ValidString(body))
	assert.NotContains(t, body, "\x00")
	assert.LessOrEqual(t, len(body), maxErrorBytes)
}
