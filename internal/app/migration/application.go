package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

// This records a historical relationship, never an executable job or credential.
// The source can retry one business review in several independently retained
// Workers; its last assignment file cannot describe every earlier execution.
type sourceApplicationRole struct {
	Application  string `json:"application"`
	AssignmentID string `json:"source_assignment_id"`
	SourceSHA256 string `json:"source_sha256"`
}

func (c *messageConverter) applicationRoles(source legacy.Agent) (map[string]sourceApplicationRole, error) {
	roles := map[string]sourceApplicationRole{}
	const prefix = "modules/memory-client/workers/"
	for _, file := range source.Files {
		if !strings.HasPrefix(file.Path, prefix) {
			continue
		}
		name := strings.TrimPrefix(file.Path, prefix)
		thread := strings.TrimSuffix(name, ".json")
		metadata, exists := c.threads[thread]
		if !exists || thread == "0" || metadata.ParentThreadID == "" || path.Base(name) != name || !strings.HasSuffix(name, ".json") {
			return nil, errors.New("source Memory assignment file does not belong to a Worker")
		}
		verified, err := c.sourceFile(file.Path, file.SHA256)
		if err != nil {
			return nil, err
		}
		// Match the fixed source LoadWorkerAssignment contract. The token is
		// validated as source evidence, then discarded rather than rebound.
		var assignment struct {
			ID        string          `json:"id"`
			Token     string          `json:"token"`
			ExpiresAt time.Time       `json:"expires_at"`
			Proposal  json.RawMessage `json:"proposal"`
			Scope     json.RawMessage `json:"scope"`
			Automatic bool            `json:"automatic"`
		}
		decoder := json.NewDecoder(bytes.NewReader(verified.Data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&assignment); err != nil {
			return nil, errors.New("invalid source Memory Worker assignment")
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF || assignment.ID == "" || assignment.Token == "" || assignment.ExpiresAt.IsZero() {
			return nil, errors.New("incomplete source Memory Worker assignment")
		}
		roles[thread] = sourceApplicationRole{Application: "memory", AssignmentID: assignment.ID, SourceSHA256: verified.SHA256}
	}
	return roles, nil
}
