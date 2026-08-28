package shared

// Internal-call header names rest-call and sql-query both attach to every
// outbound request they make to another platform service's own internal API.
const (
	InternalTokenHeader = "x-internal-token"
	DepartmentsHeader   = "x-departments"
)
