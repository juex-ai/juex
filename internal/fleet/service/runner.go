package fleetservice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const commandOutputLimit = 32 * 1024

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, spec command) (string, error) {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	cmd.Env = mergeEnvironment(os.Environ(), spec.env)
	buffer := &limitedBuffer{limit: commandOutputLimit}
	cmd.Stdout = buffer
	cmd.Stderr = buffer
	err := cmd.Run()
	return buffer.String(), err
}

type limitedBuffer struct {
	builder strings.Builder
	limit   int
	seen    int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.seen += len(p)
	remaining := b.limit - b.builder.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.builder.Write(p[:remaining])
		} else {
			_, _ = b.builder.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	text := b.builder.String()
	if b.seen > b.builder.Len() {
		text += "\n[output truncated]"
	}
	return text
}

func mergeEnvironment(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return base
	}
	values := make(map[string]string, len(base)+len(overlay))
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range overlay {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func commandFailure(cmd command, output string, err error) error {
	line := strings.TrimSpace(strings.Join(append([]string{cmd.name}, cmd.args...), " "))
	detail := boundedOutput(output)
	if detail == "" {
		return fmt.Errorf("fleet service: run %s: %w", line, err)
	}
	return fmt.Errorf("fleet service: run %s: %w: %s", line, err, detail)
}

func boundedOutput(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= commandOutputLimit {
		return output
	}
	return output[:commandOutputLimit] + " [truncated]"
}
