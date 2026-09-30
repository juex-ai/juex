package application

// ValidationError carries an intentionally public business validation reason.
// Database, transport and provider errors must never be converted to this type.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return ErrInvalid.Error() + ": " + e.Reason }
func (e *ValidationError) Unwrap() error { return ErrInvalid }
