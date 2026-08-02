package provider

import "fmt"

type HTTPError struct {
	Provider   string
	StatusCode int
	Code       string
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s returned status %d (%s)", e.Provider, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("%s returned status %d", e.Provider, e.StatusCode)
}
