package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIndexByType_DuplicateTypePanics(t *testing.T) {
	t.Parallel()
	d := Definition{Type: TypeStorage}
	assert.PanicsWithValue(t, `registry: duplicate connector Type "storage"`, func() {
		indexByType([]Definition{d, d})
	})
}
