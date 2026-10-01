package execprotocol

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

type ExtensionContext struct {
	BindingID string `json:"binding_id"`
	Directory string `json:"directory"`
}

func (e *ExtensionContext) Validate() error {
	if e == nil {
		return nil
	}
	id, err := uuid.Parse(e.BindingID)
	if err != nil || id == uuid.Nil || id.String() != e.BindingID || !path.IsAbs(e.Directory) || len(e.Directory) > 4096 || strings.ContainsRune(e.Directory, 0) {
		return ErrInvalid
	}
	return nil
}

type ObservableParser struct {
	Type             string `json:"type"`
	ContentField     string `json:"content_field,omitempty"`
	KindField        string `json:"kind_field,omitempty"`
	SeverityField    string `json:"severity_field,omitempty"`
	TimeField        string `json:"time_field,omitempty"`
	AttachmentsField string `json:"attachments_field,omitempty"`
}

type ObservableFilter struct {
	Contains string `json:"contains,omitempty"`
	Regex    string `json:"regex,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type ObservableBatch struct {
	IntervalSeconds int `json:"interval_seconds,omitempty"`
	MaxChars        int `json:"max_chars,omitempty"`
}

type ObservableOptions struct {
	Streams  []string           `json:"streams,omitempty"`
	Parser   ObservableParser   `json:"parser"`
	Filters  []ObservableFilter `json:"filters,omitempty"`
	Batch    ObservableBatch    `json:"batch"`
	OnExit   string             `json:"on_exit,omitempty"`
	Kind     string             `json:"kind,omitempty"`
	Severity string             `json:"severity,omitempty"`
}

func (o ObservableOptions) Validate() error {
	if len(o.Streams) > 2 || len(o.Filters) > 16 || !slices.Contains([]string{"", "text", "jsonl"}, o.Parser.Type) || !slices.Contains([]string{"", "always", "nonzero", "never"}, o.OnExit) || o.Batch.IntervalSeconds < 0 || o.Batch.IntervalSeconds > 86400 || o.Batch.MaxChars < 0 || o.Batch.MaxChars > 1000 || len(o.Kind) > 128 || len(o.Severity) > 64 {
		return ErrInvalid
	}
	for _, s := range o.Streams {
		if s != "stdout" && s != "stderr" {
			return ErrInvalid
		}
	}
	for _, f := range o.Filters {
		if (f.Contains == "") == (f.Regex == "") || len(f.Contains) > 1024 || len(f.Regex) > 1024 || len(f.Kind) > 128 || len(f.Severity) > 64 {
			return ErrInvalid
		}
		if f.Regex != "" {
			if _, err := regexp.Compile(f.Regex); err != nil {
				return ErrInvalid
			}
		}
	}
	for _, field := range []string{o.Parser.ContentField, o.Parser.KindField, o.Parser.SeverityField, o.Parser.TimeField, o.Parser.AttachmentsField} {
		if len(field) > 128 {
			return ErrInvalid
		}
	}
	return nil
}

type ObservableCommand struct {
	Command          []string          `json:"command"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Extension        *ExtensionContext `json:"extension,omitempty"`
	Options          ObservableOptions `json:"options"`
}
