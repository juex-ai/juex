package sandbox

import "fmt"

type ErrorCode string

const (
	ErrorCodeUnsupportedPlatform ErrorCode = "unsupported_platform"
	ErrorCodeBackendUnavailable  ErrorCode = "backend_unavailable"
	ErrorCodePolicyUnavailable   ErrorCode = "policy_unavailable"
)

type Error struct {
	Code       ErrorCode
	Platform   string
	Backend    string
	Phase      string
	Policy     Policy
	Suggestion string
	Err        error
}

func NewError(code ErrorCode, platform, backend, phase string, policy Policy, suggestion string, err error) *Error {
	return &Error{
		Code:       code,
		Platform:   platform,
		Backend:    backend,
		Phase:      phase,
		Policy:     policy,
		Suggestion: suggestion,
		Err:        err,
	}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	message := fmt.Sprintf("sandbox unavailable: platform=%s backend=%s phase=%s %s. %s", e.Platform, e.Backend, e.Phase, requestedPolicyText(e.Policy), e.Suggestion)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
