package sendemail

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// recordable keeps valid UTF-8 without NUL as is, repairs the rest, and cuts
// at most maxRecordedBytes on a rune boundary.
func TestRecordable(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "plain", recordable("plain"))
	assert.Equal(t, "ab�c", recordable("a\x00b\xffc"))

	exact := strings.Repeat("a", maxRecordedBytes)
	assert.Equal(t, exact, recordable(exact))

	// One byte short of the cap, then a 3-byte rune: cutting at the cap would
	// split the rune, so the cut moves back to its start.
	split := strings.Repeat("a", maxRecordedBytes-1) + "€" + "tail"
	got := recordable(split)
	assert.Equal(t, strings.Repeat("a", maxRecordedBytes-1), got)
	assert.True(t, utf8.ValidString(got))
}
