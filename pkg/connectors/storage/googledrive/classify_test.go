package googledrive

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/documents"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/shared"
)

func TestClassify_DriveErrors(t *testing.T) {
	t.Parallel()
	rateLimited := &googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{Reason: "userRateLimitExceeded"}}}
	forbidden := &googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{Reason: "insufficientFilePermissions"}}}

	for name, tc := range map[string]struct {
		err  error
		want shared.Class
	}{
		"403 rate limit":     {rateLimited, shared.ClassTransient},
		"403 permission":     {forbidden, shared.ClassPermanent},
		"404":                {mapNotFound(&googleapi.Error{Code: http.StatusNotFound}), shared.ClassPermanent},
		"429":                {&googleapi.Error{Code: http.StatusTooManyRequests}, shared.ClassTransient},
		"503":                {&googleapi.Error{Code: http.StatusServiceUnavailable}, shared.ClassTransient},
		"400":                {&googleapi.Error{Code: http.StatusBadRequest}, shared.ClassPermanent},
		"upload in progress": {&documents.InProgressError{DocumentID: "d"}, shared.ClassTransient},
		"ownership lost":     {documents.ErrOwnershipLost, shared.ClassTransient},
		"unrecognised":       {errors.New("odd"), shared.ClassUnknown},
	} {
		wrapped := upstream("drive fetch", fmt.Errorf("call: %w", tc.err))
		class, reason := shared.ClassOf(wrapped)
		assert.Equalf(t, tc.want, class, "%s (%s)", name, reason)
		assert.True(t, errors.Is(wrapped, shared.ErrUpstream))
	}
}

// Errors straight from Drive keep their class through every call path —
// Upload, Fetch and Delete — not only through upstream() itself.
func TestDrive_CallPaths_ClassifyDriveErrors(t *testing.T) {
	t.Parallel()
	rateLimited := &googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{Reason: "rateLimitExceeded"}}}
	notFound := &googleapi.Error{Code: http.StatusNotFound}

	for name, tc := range map[string]struct {
		driveErr error
		want     shared.Class
	}{
		"rate limited": {rateLimited, shared.ClassTransient},
		"503":          {&googleapi.Error{Code: http.StatusServiceUnavailable}, shared.ClassTransient},
		"404":          {notFound, shared.ClassPermanent},
	} {
		ctx, store := tenantCtx(), documents.NewMemoryStore()

		// Upload: the write fails with the Drive error.
		api := newFakeDriveFilesAPI()
		api.err = tc.driveErr
		err := newTestClient(api, store).Upload(ctx, folder, "u.pdf", []byte("x"), "")
		class, reason := shared.ClassOf(err)
		assert.Equalf(t, tc.want, class, "upload %s (%s)", name, reason)

		// Fetch and Delete of a recorded document: the Drive call fails.
		api = newFakeDriveFilesAPI()
		c := newTestClient(api, store)
		require.NoError(t, c.Upload(ctx, folder, "f.pdf", []byte("x"), ""))
		api.err = tc.driveErr
		_, _, err = c.Fetch(ctx, folder, "f.pdf", 50<<20)
		class, reason = shared.ClassOf(err)
		assert.Equalf(t, tc.want, class, "fetch %s (%s)", name, reason)
		err = c.Delete(ctx, folder, "f.pdf")
		class, reason = shared.ClassOf(err)
		assert.Equalf(t, tc.want, class, "delete %s (%s)", name, reason)
	}
}
