package shared

import (
	"encoding/json"
	"fmt"
)

// googleTokenEndpoints are the only token endpoints a tenant's Google
// service-account key may name.
var googleTokenEndpoints = map[string]bool{
	"https://oauth2.googleapis.com/token":      true,
	"https://oauth2.mtls.googleapis.com/token": true,
}

// ValidateGoogleServiceAccountKey checks a tenant-supplied Google
// service-account key before any Google library reads it. The libraries take
// the token endpoint from the key itself (token_uri) and POST a signed
// assertion there, returning the response body in their errors — so an
// unchecked key would let a tenant aim the worker at any host it can reach
// (SSRF) and read the reply in the task error. Only a service-account key
// for Google's own endpoint and universe is accepted.
func ValidateGoogleServiceAccountKey(raw []byte, field string) error {
	var key struct {
		Type           string `json:"type"`
		TokenURI       string `json:"token_uri"`
		UniverseDomain string `json:"universe_domain"`
	}
	if err := json.Unmarshal(raw, &key); err != nil {
		return fmt.Errorf("%w: %s is not valid JSON", ErrValidation, field)
	}
	if key.Type != "service_account" {
		return fmt.Errorf("%w: %s must be a service_account key", ErrValidation, field)
	}
	if key.TokenURI != "" && !googleTokenEndpoints[key.TokenURI] {
		return fmt.Errorf("%w: %s token_uri must be Google's token endpoint (https://oauth2.googleapis.com/token)", ErrValidation, field)
	}
	if key.UniverseDomain != "" && key.UniverseDomain != "googleapis.com" {
		return fmt.Errorf("%w: %s universe_domain must be googleapis.com", ErrValidation, field)
	}
	return nil
}
