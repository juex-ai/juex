package protocol

import (
	"errors"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	openaisdk "github.com/openai/openai-go"
)

type providerHTTPError struct {
	status int
	cause  error
}

func (e *providerHTTPError) Error() string { return e.cause.Error() }

func (e *providerHTTPError) Unwrap() error { return e.cause }

func (e *providerHTTPError) HTTPStatusCode() int { return e.status }

// WrapProviderError preserves SDK causes while publishing the neutral HTTP status fact.
func WrapProviderError(err error) error {
	if err == nil {
		return nil
	}
	var existing interface{ HTTPStatusCode() int }
	if errors.As(err, &existing) {
		return err
	}
	var anthropicErr *anthropicsdk.Error
	if errors.As(err, &anthropicErr) {
		return &providerHTTPError{status: anthropicErr.StatusCode, cause: err}
	}
	var openaiErr *openaisdk.Error
	if errors.As(err, &openaiErr) {
		return &providerHTTPError{status: openaiErr.StatusCode, cause: err}
	}
	return err
}
