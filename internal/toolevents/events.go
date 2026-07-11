package toolevents

import "github.com/juex-ai/juex/internal/llm"

const (
	RequestedType   = "tool.requested"
	OutputDeltaType = "tool.output_delta"
	CompletedType   = "tool.completed"
	ErroredType     = "tool.errored"
)

type ToolCallPayload struct {
	ToolUseID      string         `json:"tool_use_id"`
	Name           string         `json:"name"`
	Input          map[string]any `json:"input"`
	TimeoutSeconds int            `json:"timeout_seconds"`
}

type RequestedPayload struct {
	Name           string         `json:"name"`
	Input          map[string]any `json:"input"`
	ToolUseID      string         `json:"tool_use_id"`
	TimeoutSeconds int            `json:"timeout_seconds"`
}

type OutputDelta struct {
	Name          string
	ToolUseID     string
	SessionID     string
	ChunkID       int
	Stream        string
	Text          string
	Truncated     bool
	BinaryOmitted bool
	BinaryBytes   int
	BinarySHA256  string
	FirstBytesHex string
}

type OutputDeltaPayload struct {
	Name          string `json:"name"`
	ToolUseID     string `json:"tool_use_id"`
	SessionID     string `json:"session_id"`
	ChunkID       int    `json:"chunk_id"`
	Stream        string `json:"stream"`
	Text          string `json:"text"`
	Truncated     bool   `json:"truncated,omitempty"`
	BinaryOmitted bool   `json:"binary_omitted,omitempty"`
	BinaryBytes   int    `json:"binary_bytes,omitempty"`
	BinarySHA256  string `json:"binary_sha256,omitempty"`
	FirstBytesHex string `json:"first_bytes_hex,omitempty"`
}

type CompletedPayload struct {
	Name           string        `json:"name"`
	ToolUseID      string        `json:"tool_use_id"`
	TimeoutSeconds int           `json:"timeout_seconds"`
	Len            int           `json:"len"`
	Preview        string        `json:"preview"`
	Result         any           `json:"result,omitempty"`
	Media          *llm.MediaRef `json:"media,omitempty"`
}

type ErroredPayload struct {
	Name           string        `json:"name"`
	ToolUseID      string        `json:"tool_use_id"`
	Error          string        `json:"error"`
	ErrorKind      string        `json:"error_kind,omitempty"`
	RawCause       string        `json:"raw_cause,omitempty"`
	TimeoutSeconds int           `json:"timeout_seconds"`
	Len            int           `json:"len,omitempty"`
	Preview        string        `json:"preview,omitempty"`
	TimedOut       bool          `json:"timed_out,omitempty"`
	ExitCode       *int          `json:"exit_code,omitempty"`
	Result         any           `json:"result,omitempty"`
	Media          *llm.MediaRef `json:"media,omitempty"`
}

type ErroredOptions struct {
	Error          string
	ErrorKind      string
	RawCause       string
	TimeoutSeconds int
	Len            int
	Preview        string
	TimedOut       bool
	ExitCode       *int
	Result         any
	Media          *llm.MediaRef
}

func Requested(call ToolCallPayload) RequestedPayload {
	return RequestedPayload{
		Name:           call.Name,
		Input:          call.Input,
		ToolUseID:      call.ToolUseID,
		TimeoutSeconds: call.TimeoutSeconds,
	}
}

func Delta(call ToolCallPayload, delta OutputDelta) OutputDeltaPayload {
	name := delta.Name
	if name == "" {
		name = call.Name
	}
	toolUseID := delta.ToolUseID
	if toolUseID == "" {
		toolUseID = call.ToolUseID
	}
	return OutputDeltaPayload{
		Name:          name,
		ToolUseID:     toolUseID,
		SessionID:     delta.SessionID,
		ChunkID:       delta.ChunkID,
		Stream:        delta.Stream,
		Text:          delta.Text,
		Truncated:     delta.Truncated,
		BinaryOmitted: delta.BinaryOmitted,
		BinaryBytes:   delta.BinaryBytes,
		BinarySHA256:  delta.BinarySHA256,
		FirstBytesHex: delta.FirstBytesHex,
	}
}

func Completed(call ToolCallPayload, timeoutSeconds int, outputLen int, preview string, result any) CompletedPayload {
	return CompletedPayload{
		Name:           call.Name,
		ToolUseID:      call.ToolUseID,
		TimeoutSeconds: timeoutSeconds,
		Len:            outputLen,
		Preview:        preview,
		Result:         result,
	}
}

func Errored(call ToolCallPayload, opts ErroredOptions) ErroredPayload {
	return ErroredPayload{
		Name:           call.Name,
		ToolUseID:      call.ToolUseID,
		Error:          opts.Error,
		ErrorKind:      opts.ErrorKind,
		RawCause:       opts.RawCause,
		TimeoutSeconds: opts.TimeoutSeconds,
		Len:            opts.Len,
		Preview:        opts.Preview,
		TimedOut:       opts.TimedOut,
		ExitCode:       opts.ExitCode,
		Result:         opts.Result,
		Media:          opts.Media,
	}
}
