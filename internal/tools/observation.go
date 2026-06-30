package tools

type Observation struct {
	ToolName         string
	ToolUseID        string
	Input            map[string]any
	Content          string
	Error            string
	TimedOut         bool
	ExitCode         *int
	StructuredResult any
}

type ObservationOptions struct {
	ToolName         string
	ToolUseID        string
	Input            map[string]any
	Content          string
	Err              error
	TimedOut         bool
	ExitCode         *int
	StructuredResult any
}

type structuredExitCodeResult interface {
	ToolCallExitCode() (int, bool)
}

func NewObservation(opts ObservationOptions) Observation {
	obs := Observation{
		ToolName:         opts.ToolName,
		ToolUseID:        opts.ToolUseID,
		Input:            cloneCallInput(opts.Input),
		Content:          opts.Content,
		TimedOut:         opts.TimedOut || structuredResultTimedOut(opts.StructuredResult),
		ExitCode:         cloneIntPtr(opts.ExitCode),
		StructuredResult: opts.StructuredResult,
	}
	if opts.Err != nil {
		obs.Error = opts.Err.Error()
	}
	if obs.ExitCode == nil {
		if code, ok := structuredResultExitCode(opts.StructuredResult); ok {
			obs.ExitCode = &code
		}
	}
	if obs.ExitCode == nil {
		if code, ok := ExitCodeFromError(opts.Err); ok {
			obs.ExitCode = &code
		}
	}
	return obs
}

func (o Observation) Clone() Observation {
	o.Input = cloneCallInput(o.Input)
	o.ExitCode = cloneIntPtr(o.ExitCode)
	return o
}

func (o Observation) WithRuntimeContext(toolName, toolUseID string, input map[string]any, content string, err error) Observation {
	o = o.Clone()
	if o.ToolName == "" {
		o.ToolName = toolName
	}
	if o.ToolUseID == "" {
		o.ToolUseID = toolUseID
	}
	if len(o.Input) == 0 && input != nil {
		o.Input = cloneCallInput(input)
	}
	if o.Content == "" && content != "" {
		o.Content = content
	}
	if err != nil {
		o.Error = err.Error()
		if o.ExitCode == nil {
			if code, ok := ExitCodeFromError(err); ok {
				o.ExitCode = &code
			}
		}
	}
	return o
}

func structuredResultExitCode(result any) (int, bool) {
	reporter, ok := result.(structuredExitCodeResult)
	if !ok {
		return 0, false
	}
	return reporter.ToolCallExitCode()
}
