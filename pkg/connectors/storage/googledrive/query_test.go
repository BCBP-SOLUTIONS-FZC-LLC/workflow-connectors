package googledrive

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDriveQuote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, `'report.pdf'`, driveQuote("report.pdf"))
	assert.Equal(t, `'O\'Brien.pdf'`, driveQuote("O'Brien.pdf"))
	assert.Equal(t, `'a\\b'`, driveQuote(`a\b`))
	assert.Equal(t, `'x\' or name contains \''`, driveQuote("x' or name contains '"),
		"a quote in a name cannot end the literal and inject query terms")
	assert.Equal(t, `'café'`, driveQuote("café"))
}
