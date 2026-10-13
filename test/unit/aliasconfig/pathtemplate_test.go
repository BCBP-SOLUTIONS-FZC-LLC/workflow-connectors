package aliasconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/aliasconfig"
)

func restEndpoint(path string) aliasconfig.Config {
	return aliasconfig.Config{RestCall: []aliasconfig.Endpoint{{
		Alias: "a", Method: "GET", BaseURL: "https://svc.internal", PathTemplate: path,
	}}}
}

// Everything after "#" is a fragment, never sent: "/x#?role=user" fixes no
// query key, so a caller's queryParams could set role. Refused anywhere in
// the template, placeholder names included; a literal "#" is %23.
func TestValidate_PathTemplateFragmentIsRejected(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/x#?role=user", "/x#", "/x?a=1#frag", "/x/{a#b}", "/#"} {
		err := restEndpoint(path).Validate()
		require.Error(t, err, path)
		assert.Contains(t, err.Error(), `must not contain "#"`, path)
	}
	require.NoError(t, restEndpoint("/tags/%23urgent?q=%23x").Validate())
}

// A character that is not valid in a URL is refused outside a placeholder.
func TestValidate_PathTemplateInvalidCharacterIsRejected(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/a b", "/x?a=b c", "/x\t", "/x\n", "/x\x00", "/x\x7f",
		`/x"`, "/x<", "/x>", `/x\y`, "/x^", "/x`", "/x|y",
	} {
		err := restEndpoint(path).Validate()
		require.Error(t, err, "%q", path)
		assert.Contains(t, err.Error(), "not valid in a URL", "%q", path)
	}
	// Placeholder names are not part of the URL: the value is escaped.
	require.NoError(t, restEndpoint("/items/{item id}?q={a|b}").Validate())
}

func TestValidate_PathTemplateUnterminatedPlaceholderIsRejected(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/items/{id", "/x?a={v", "/{"} {
		err := restEndpoint(path).Validate()
		require.Error(t, err, path)
		assert.Contains(t, err.Error(), `unterminated "{" placeholder`, path)
	}
}

// A "%" in the path part must start a valid escape: "/x/%{id}" with id "2F"
// would otherwise render an encoded slash.
func TestValidate_PathTemplateBadPathEscapeIsRejected(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/x/%{id}", "/x/%2{id}", "/x/%zz", "/x/%", "/x/%4?a=1"} {
		err := restEndpoint(path).Validate()
		require.Error(t, err, path)
		assert.Contains(t, err.Error(), "invalid escape", path)
	}
	for _, path := range []string{"/x/%20{id}", "/x/a%2Fb", "/x/{id}%41", "/x/{id}?a=%20"} {
		require.NoError(t, restEndpoint(path).Validate(), path)
	}
}
