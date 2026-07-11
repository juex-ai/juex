package observable

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/tools"
)

type ParsedUnit struct {
	Stream           string
	Content          string
	Kind             string
	Severity         string
	Attachments      []eventmedia.AttachmentRef
	AttachmentErrors []string
	ReceivedAt       time.Time
}

type PipelineOptions struct {
	Now func() time.Time
}

type Pipeline struct {
	spec    Spec
	now     func() time.Time
	filters []compiledFilter
	buffers map[string]string
}

type compiledFilter struct {
	spec  FilterSpec
	regex *regexp.Regexp
}

func NewPipeline(spec Spec, opts ...PipelineOptions) (*Pipeline, error) {
	normalized, err := ValidateSpec(spec)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if len(opts) > 0 && opts[0].Now != nil {
		now = opts[0].Now
	}
	p := &Pipeline{
		spec:    normalized,
		now:     now,
		buffers: map[string]string{},
	}
	for _, filter := range normalized.Filters {
		cf := compiledFilter{spec: filter}
		if filter.Regex != "" {
			re, err := regexp.Compile(filter.Regex)
			if err != nil {
				return nil, err
			}
			cf.regex = re
		}
		p.filters = append(p.filters, cf)
	}
	return p, nil
}

func (p *Pipeline) Accept(stream string, data []byte) ([]ParsedUnit, error) {
	if p == nil || len(data) == 0 {
		return nil, nil
	}
	parserType := ParserText
	if p.spec.Parser != nil && p.spec.Parser.Type != "" {
		parserType = p.spec.Parser.Type
	}
	switch parserType {
	case ParserJSONL:
		return p.acceptJSONL(stream, data)
	default:
		text := tools.SanitizeOutputBytes(data).Text
		return p.filterUnit(ParsedUnit{
			Stream:     stream,
			Content:    text,
			Kind:       resolvedKind(p.spec.Defaults.Kind),
			Severity:   resolvedSeverity(p.spec.Defaults.Severity),
			ReceivedAt: p.now().UTC(),
		}), nil
	}
}

func (p *Pipeline) acceptJSONL(stream string, data []byte) ([]ParsedUnit, error) {
	text := p.buffers[stream] + string(data)
	lines := strings.SplitAfter(text, "\n")
	p.buffers[stream] = ""
	if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
		p.buffers[stream] = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	}
	var out []ParsedUnit
	var firstErr error
	for _, line := range lines {
		unit, ok, err := p.parseJSONLLine(stream, line)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !ok {
			continue
		}
		out = append(out, p.filterUnit(unit)...)
	}
	return out, firstErr
}

func (p *Pipeline) Flush() ([]ParsedUnit, error) {
	if p == nil || len(p.buffers) == 0 {
		return nil, nil
	}
	var out []ParsedUnit
	var firstErr error
	for stream, buffered := range p.buffers {
		delete(p.buffers, stream)
		unit, ok, err := p.parseJSONLLine(stream, buffered)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !ok {
			continue
		}
		out = append(out, p.filterUnit(unit)...)
	}
	return out, firstErr
}

func (p *Pipeline) parseJSONLLine(stream, line string) (ParsedUnit, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return ParsedUnit{}, false, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return ParsedUnit{}, false, fmt.Errorf("observable jsonl: parse line: %w", err)
	}
	unit, err := p.unitFromJSON(stream, obj, line)
	if err != nil {
		return ParsedUnit{}, false, err
	}
	return unit, true, nil
}

func (p *Pipeline) unitFromJSON(stream string, obj map[string]any, raw string) (ParsedUnit, error) {
	parser := p.spec.Parser
	content := raw
	kind := resolvedKind(p.spec.Defaults.Kind)
	severity := resolvedSeverity(p.spec.Defaults.Severity)
	var attachments []eventmedia.AttachmentRef
	var attachmentErrors []string
	if parser != nil {
		if parser.ContentField != "" {
			if value, ok := obj[parser.ContentField]; ok {
				content = fmt.Sprint(value)
			}
		}
		if parser.KindField != "" {
			if value, ok := obj[parser.KindField].(string); ok && value != "" {
				kind = value
			}
		}
		if parser.SeverityField != "" {
			if value, ok := obj[parser.SeverityField].(string); ok {
				if normalized, ok := normalizeSeverityValue(value); ok {
					severity = normalized
				}
			}
		}
		if parser.AttachmentsField != "" {
			if value, ok := obj[parser.AttachmentsField]; ok {
				refs, err := eventmedia.ExtractAttachmentRefs(value)
				if err != nil {
					attachmentErrors = append(attachmentErrors, fmt.Sprintf("attachments_field %q: %v", parser.AttachmentsField, err))
				} else {
					attachments = refs
				}
			}
		}
	}
	content = tools.SanitizeOutputText(content).Text
	return ParsedUnit{
		Stream:           stream,
		Content:          content,
		Kind:             kind,
		Severity:         severity,
		Attachments:      attachments,
		AttachmentErrors: attachmentErrors,
		ReceivedAt:       p.now().UTC(),
	}, nil
}

func (p *Pipeline) filterUnit(unit ParsedUnit) []ParsedUnit {
	if len(p.filters) == 0 {
		return []ParsedUnit{unit}
	}
	var out []ParsedUnit
	for _, filter := range p.filters {
		if !filter.matches(unit.Content) {
			continue
		}
		next := unit
		if filter.spec.Kind != "" {
			next.Kind = filter.spec.Kind
		}
		if filter.spec.Severity != "" {
			next.Severity = filter.spec.Severity
		}
		out = append(out, next)
	}
	return out
}

func (f compiledFilter) matches(content string) bool {
	if f.spec.Contains != "" {
		return strings.Contains(content, f.spec.Contains)
	}
	if f.regex != nil {
		return f.regex.MatchString(content)
	}
	return false
}

func validSeverityValue(value string) bool {
	return value == "info" || value == "warning" || value == "error" || value == "critical"
}

func normalizeSeverityValue(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "warn" {
		value = "warning"
	}
	if !validSeverityValue(value) {
		return "", false
	}
	return value, true
}
