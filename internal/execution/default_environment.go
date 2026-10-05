package execution

import (
	"context"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

// DefaultEnvironment selects the location of new work. It grants no authority
// and never changes the environment of a prepared operation or connection.
type DefaultEnvironment struct {
	EnvironmentID    string `json:"environment_id"`
	WorkingDirectory string `json:"working_directory"`
	Version          int64  `json:"version"`
}

func (v DefaultEnvironment) Validate() error {
	if v.Version < 0 || len(v.WorkingDirectory) > 4096 || strings.ContainsRune(v.WorkingDirectory, 0) || v.WorkingDirectory != "" && !path.IsAbs(v.WorkingDirectory) {
		return execprotocol.ErrInvalid
	}
	if v.EnvironmentID != "" {
		id, err := uuid.Parse(v.EnvironmentID)
		if err != nil || id == uuid.Nil || id.String() != v.EnvironmentID {
			return execprotocol.ErrInvalid
		}
	} else if v.WorkingDirectory != "" {
		return execprotocol.ErrInvalid
	}
	return nil
}

func (s *Service) DefaultEnvironment(ctx context.Context, actor, tenant, agent string) (DefaultEnvironment, error) {
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return DefaultEnvironment{}, err
	}
	return s.Store.DefaultEnvironment(ctx, scope)
}

func (s *Service) SetDefaultEnvironment(ctx context.Context, actor, tenant, agent string, value DefaultEnvironment) (DefaultEnvironment, error) {
	done, err := maintenance.Enter(s.Admission)
	if err != nil {
		return DefaultEnvironment{}, err
	}
	defer done()
	if err := value.Validate(); err != nil {
		return DefaultEnvironment{}, err
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return DefaultEnvironment{}, err
	}
	if !scope.CanExecute {
		return DefaultEnvironment{}, execprotocol.ErrDenied
	}
	return s.Store.SetDefaultEnvironment(ctx, scope, value)
}
