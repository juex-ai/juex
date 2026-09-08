package agent

// TurnWarning is a non-blocking application warning rendered by transports.
type TurnWarning struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}
