package connectors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDocRefStore_RegisterThenResolve_RoundTrips(t *testing.T) {
	t.Parallel()

	store := newDocRefStore()
	store.register("ref-1", []byte("hello"), "text/plain")

	content, contentType, found := store.resolve("ref-1")
	assert.True(t, found)
	assert.Equal(t, []byte("hello"), content)
	assert.Equal(t, "text/plain", contentType)
}

func TestDocRefStore_UnknownRef_NotFound(t *testing.T) {
	t.Parallel()

	store := newDocRefStore()
	_, _, found := store.resolve("never-registered")
	assert.False(t, found)
}
