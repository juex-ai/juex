package migration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/execution/host"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// ApplyConfig comes from independently verified operator configuration, never
// from a bundle's destination claims. The source services and their children
// must already be stopped with restart disabled. Apply consumes Lock; a parent
// operator retains its own descriptor and leaves the deployment drained.
type ApplyConfig struct {
	Target               BundleTarget        `json:"-"`
	Source               *legacy.SourceGuard `json:"-"`
	Lock                 *os.File            `json:"-"`
	MaintenanceDirectory string              `json:"-"`
	DatabaseURL          string              `json:"-"`
	MasterKey            []byte              `json:"-"`
	PublicURL            string              `json:"-"`
	BlobDirectory        string              `json:"-"`
	BlobCapacity         int64               `json:"-"`
	HostBackend          host.Config         `json:"-"`
	HostKey              []byte              `json:"-"`
	verifyTarget         func() error
}

// ApplyReport records inert imports. Environment selection, extension resource
// installation and actual application acceptance are separate cutover steps.
type ApplyReport struct {
	ConversionPolicy     int                                    `json:"conversion_policy"`
	InputTrackingThreads int                                    `json:"input_tracking_threads"`
	BundleSHA256         string                                 `json:"bundle_sha256"`
	Agents               map[string]string                      `json:"agents,omitempty"`
	Threads              map[string]map[string]string           `json:"threads,omitempty"`
	Stages               []string                               `json:"stages,omitempty"`
	Artifacts            int                                    `json:"artifacts"`
	Files                map[string]execution.HostImportReceipt `json:"files,omitempty"`
	Imported             bool                                   `json:"imported"`
}

// Apply imports owner state while keeping source locks and destination
// maintenance in force. It starts no service, scheduler, provider or executor,
// runs no schema migration and never compensates by deleting committed imports.
// An exact retry reconstructs requests solely from the original frozen bundle.
func Apply(ctx context.Context, b *Bundle, config ApplyConfig) (report ApplyReport, resultErr error) {
	if b == nil || b.header.Target != config.Target || config.Source == nil || config.Lock == nil || config.DatabaseURL == "" || config.PublicURL == "" || !filepath.IsAbs(config.BlobDirectory) || config.BlobCapacity < 1 {
		return report, errors.New("offline import requires a matching verified target, source guard and operator configuration")
	}
	pgConfig, err := offlineDatabaseConfig(config.DatabaseURL)
	if err != nil {
		return report, err
	}
	prepared, err := b.Prepare()
	if err != nil {
		return report, err
	}
	if b.source.Memory == nil {
		return report, errors.New("complete Fleet migration requires source Memory state")
	}
	if err := b.VerifySource(config.Source); err != nil {
		return report, err
	}
	box, err := secrets.New(config.MasterKey)
	if err != nil {
		return report, err
	}
	gate, err := maintenance.Open(config.MaintenanceDirectory)
	if err != nil {
		return report, err
	}
	done, err := gate.InheritExclusive(config.Lock)
	if err != nil {
		return report, err
	}
	defer done()
	if config.verifyTarget != nil {
		if err := config.verifyTarget(); err != nil {
			return report, err
		}
	}
	pgConfig.MaxConns, pgConfig.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, pgConfig)
	if err != nil {
		return report, errors.New("cannot open offline database")
	}
	defer pool.Close()
	check := func() error {
		if config.verifyTarget != nil {
			if err := config.verifyTarget(); err != nil {
				return err
			}
		}
		if err := config.Source.Check(); err != nil {
			return err
		}
		return managed.RequireOffline(ctx, pool)
	}
	if err := check(); err != nil {
		return report, err
	}
	reports, err := importInventory(ctx, pool)
	if err != nil {
		return report, err
	}
	// Uploads can be this import's interrupted Blob publication. Every such ID
	// must be covered by a real owner receipt and exact request below, before
	// any bytes are resumed. Other unsettled effects never receive an exception.
	pending := map[string]bool{}
	for _, r := range reports {
		for _, item := range r.Busy {
			if r.Service != "execution" || item.Kind != "artifact" {
				return report, errors.New("offline import requires settled external operations")
			}
			pending[item.ID] = true
		}
		if r.Service == "runtime" && len(r.Review) != 0 {
			return report, errors.New("offline import requires settled Runtime work")
		}
	}
	directory := managementpg.NewDirectory(pool, managementpg.Config{Secrets: box, PublicURL: config.PublicURL})
	authority := managed.RuntimeAuthority{Directory: directory}
	target := config.Target
	access := application.Access{ActorID: target.ActorID, TenantID: target.TenantID, UserID: target.UserID}
	owner, err := authority.AuthorizeApplication(ctx, access, true)
	if err != nil {
		return report, err
	}
	if owner.FleetID != target.FleetID {
		return report, errors.New("target Fleet identity changed")
	}
	report.BundleSHA256 = b.digest
	report.ConversionPolicy = b.conversionPolicy
	modelIDs, recoveredV1, err := publishModels(ctx, directory, target.TenantID, "juex/281889e5/fleet/"+b.source.ID, b.digest, prepared.Models)
	if err != nil {
		return report, err
	}
	report.Stages = append(report.Stages, "models")
	if err := check(); err != nil {
		return report, err
	}
	request := management.AgentsImport{ExpectedFleetID: target.FleetID, Source: "juex/281889e5/fleet/" + b.source.ID, SourceSHA256: b.digest}
	for _, binding := range prepared.Models.Agents {
		value := prepared.Agents[binding.SourceAgentID]
		value.Configuration.Models = nil
		for _, key := range binding.Models {
			value.Configuration.Models = append(value.Configuration.Models, modelIDs[key].ID)
		}
		request.Agents = append(request.Agents, management.ImportedAgent{SourceAgentID: binding.SourceAgentID, Config: value, Environment: prepared.Environments[binding.SourceAgentID], AgentManagement: prepared.AgentManagement[binding.SourceAgentID]})
	}
	if recoveredV1 && b.conversionPolicy == 2 {
		if _, err := agentProofV1(request); err != nil {
			return report, err
		}
	}
	report.Agents, err = directory.ImportAgents(ctx, target.ActorID, target.TenantID, target.UserID, request)
	if errors.Is(err, management.ErrImportProofVersion) && recoveredV1 && b.conversionPolicy == 2 {
		proof, proofErr := agentProofV1(request)
		if proofErr != nil {
			return report, proofErr
		}
		report.Agents, err = directory.RecoverAgentImportV1(ctx, target.ActorID, target.TenantID, target.UserID, proof)
	}
	if err != nil {
		return report, err
	}
	report.Stages = append(report.Stages, "agents")
	if err := check(); err != nil {
		return report, err
	}
	executionStore := executionpg.New(pool)
	local := importExecutionAuthority{directory: directory}
	for _, agent := range b.source.Agents {
		scope, err := local.Agent(ctx, target.ActorID, target.TenantID, report.Agents[agent.Definition.ID], true)
		if err != nil {
			return report, err
		}
		for id := range pending {
			artifact, err := executionStore.Artifact(ctx, scope, id)
			if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrNotFound) {
				continue
			}
			if err != nil {
				return report, err
			}
			matched := false
			for _, file := range agent.Files {
				if strings.HasPrefix(file.Path, "media/") && scope.Capabilities.Allows(agentpolicy.Files) && artifact.Scope.SameAuthority(scope) && reflect.DeepEqual(artifact.Request, b.artifactRequest(agent.Definition.ID, file)) {
					matched = true
					break
				}
			}
			if !matched {
				return report, errors.New("unfinished Artifact does not match this import")
			}
			delete(pending, id)
		}
	}
	if len(pending) != 0 {
		return report, errors.New("unrelated unfinished Artifact prevents import")
	}
	objects, err := blob.Open(config.BlobDirectory)
	if err != nil {
		return report, err
	}
	defer func() { resultErr = errors.Join(resultErr, objects.Close()) }()
	service := execution.Service{Store: executionStore, Authority: local, Blobs: &execution.ArtifactManager{Store: executionStore, Objects: objects, Capacity: config.BlobCapacity}}
	memoryBindings := MemoryBindings{SourceSHA256: b.digest, Control: b.header.MemoryControl, AdvancedSince: b.header.MemoryAdvancedSince, Agents: map[string]MemoryAgentBinding{}}
	calendarBindings := CalendarBindings{SourceSHA256: b.digest, CapturedAt: b.header.CapturedAt, Agents: map[string]application.Scope{}}
	report.Threads = map[string]map[string]string{}
	report.Files = map[string]execution.HostImportReceipt{}
	for _, agent := range b.source.Agents {
		if err := check(); err != nil {
			return report, err
		}
		id := report.Agents[agent.Definition.ID]
		fileRequest, err := b.hostFiles(agent, id, prepared)
		if err != nil {
			return report, err
		}
		var restored execution.HostImportReceipt
		if len(fileRequest.Threads)+len(fileRequest.Extensions) > 0 {
			backend, err := host.OpenExisting(config.HostBackend)
			if err != nil {
				return report, err
			}
			scope, err := local.Agent(ctx, target.ActorID, target.TenantID, id, true)
			if err != nil {
				return report, err
			}
			manager := execution.ManagedManager{Store: executionStore, Backend: backend, Key: config.HostKey}
			restored, err = manager.RestoreHostFiles(ctx, scope, fileRequest)
			if err != nil {
				return report, err
			}
			report.Files[agent.Definition.ID] = restored
			report.Stages = append(report.Stages, "files/"+agent.Definition.ID)
		}
		artifacts := map[string]execution.Artifact{}
		for _, file := range agent.Files {
			if !strings.HasPrefix(file.Path, "media/") {
				continue
			}
			upload, err := service.BeginArtifact(ctx, target.ActorID, target.TenantID, id, b.artifactRequest(agent.Definition.ID, file))
			if err != nil {
				return report, err
			}
			for offset := upload.Cursor; offset < file.Size; {
				end := min(offset+execprotocol.FileChunkBytes, file.Size)
				chunk := file.Data[offset:end]
				next, err := service.WriteArtifact(ctx, target.ActorID, target.TenantID, id, upload.Artifact.ID, execprotocol.FileChunk{Offset: offset, Data: chunk, SHA256: bundleDigest(chunk)})
				if err != nil {
					return report, err
				}
				if next.Cursor != end {
					return report, errors.New("artifact upload cursor differs from acknowledged chunk")
				}
				offset = next.Cursor
			}
			artifact, err := service.CommitArtifact(ctx, target.ActorID, target.TenantID, id, upload.Artifact.ID)
			if err != nil {
				return report, err
			}
			artifacts[file.Path] = artifact
			report.Artifacts++
		}
		if err := check(); err != nil {
			return report, err
		}
		scope, err := authority.Authorize(ctx, target.ActorID, target.TenantID, id, true)
		if err != nil {
			return report, err
		}
		origins, err := bindModelOrigins(prepared.origins[agent.Definition.ID], prepared.Models, modelIDs)
		if err != nil {
			return report, err
		}
		converted, err := convertRuntimeForPolicy(scope, agent, RuntimeBindings{SourceSHA256: b.digest, Artifacts: artifacts, ModelOrigins: origins, WorkingFiles: restoredWorkingFiles(agent, id, restored)}, b.conversionPolicy)
		if err != nil {
			return report, err
		}
		if err := runtimepg.New(pool).ImportAgent(ctx, scope, converted.Import); err != nil {
			return report, err
		}
		for _, thread := range converted.Import.Threads {
			if thread.InputTracking != nil {
				report.InputTrackingThreads++
			}
		}
		report.Threads[agent.Definition.ID] = converted.Identities.Threads
		report.Stages = append(report.Stages, "runtime/"+agent.Definition.ID)
		memoryBindings.Agents[agent.Definition.ID] = MemoryAgentBinding{Runtime: converted}
	}
	// Refresh all application scopes after Runtime imports. The old responses
	// and durable import receipts do not grant authority for another owner.
	applicationScopes := func() error {
		var err error
		owner, err = authority.AuthorizeApplication(ctx, access, true)
		if err != nil {
			return err
		}
		if owner.FleetID != target.FleetID {
			return errors.New("target Fleet identity changed")
		}
		for source, id := range report.Agents {
			agentAccess := access
			agentAccess.AgentID = id
			scope, err := authority.AuthorizeApplication(ctx, agentAccess, true)
			if err != nil {
				return err
			}
			binding := memoryBindings.Agents[source]
			binding.Scope = scope
			memoryBindings.Agents[source] = binding
			calendarBindings.Agents[source] = scope
		}
		return nil
	}
	if err := check(); err != nil {
		return report, err
	}
	if err := applicationScopes(); err != nil {
		return report, err
	}
	mem, err := ConvertMemory(owner, b.source, memoryBindings)
	if err != nil {
		return report, err
	}
	if err := memorypg.New(pool).ImportFleet(ctx, owner, mem); err != nil {
		return report, err
	}
	report.Stages = append(report.Stages, "memory")
	if err := check(); err != nil {
		return report, err
	}
	if err := applicationScopes(); err != nil {
		return report, err
	}
	cal, err := ConvertCalendar(owner, b.source, calendarBindings)
	if err != nil {
		return report, err
	}
	if err := calendarpg.New(pool).ImportFleet(ctx, owner, cal.Import); err != nil {
		return report, err
	}
	report.Stages = append(report.Stages, "calendar")
	if err := check(); err != nil {
		return report, err
	}
	reports, err = importInventory(ctx, pool)
	if err != nil {
		return report, err
	}
	for _, r := range reports {
		if !r.Ready() {
			return report, fmt.Errorf("%s import left unsettled work", r.Service)
		}
	}
	report.Imported = true
	return report, nil
}

func offlineDatabaseConfig(address string) (*pgxpool.Config, error) {
	// pgx merges libpq environment settings even with an explicit URL. This
	// private offline process must not inherit a different service or session.
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") && value != "" {
			return nil, errors.New("offline migration rejects PG environment configuration; unset PG* variables")
		}
	}
	config, err := pgxpool.ParseConfig(address)
	if err != nil {
		return nil, errors.New("invalid offline database configuration")
	}
	return config, nil
}

func (b *Bundle) artifactRequest(agent string, file legacy.SourceFile) execution.ArtifactRequest {
	return execution.ArtifactRequest{RequestID: "migration:" + bundleDigest([]byte(b.digest+"\x00"+agent+"\x00"+file.Path)), Name: filepath.Base(file.Path), MediaType: http.DetectContentType(file.Data), Visibility: "agent", Manifest: execprotocol.FileManifest{Size: file.Size, SHA256: file.SHA256}}
}

func importInventory(ctx context.Context, pool *pgxpool.Pool) ([]maintenance.Report, error) {
	var reports []maintenance.Report
	for _, inspect := range []func(context.Context, *pgxpool.Pool) (maintenance.Report, error){managementpg.MaintenanceReport, runtimepg.MaintenanceReport, executionpg.MaintenanceReport, memorypg.MaintenanceReport, calendarpg.MaintenanceReport} {
		r, err := inspect(ctx, pool)
		if err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return reports, nil
}

type importExecutionAuthority struct{ directory *managementpg.Directory }

func (a importExecutionAuthority) Owner(ctx context.Context, actor, tenant, owner string, execute bool) (execution.OwnerScope, error) {
	v, err := a.directory.AuthorizeFleet(ctx, actor, tenant, owner, execute)
	if err != nil {
		return execution.OwnerScope{}, err
	}
	return execution.OwnerScope{TenantID: v.Fleet.TenantID, UserID: v.Fleet.UserID, FleetID: v.Fleet.ID, ActorID: v.ActorID, ActorAuthorizationEpoch: v.ActorAuthorizationEpoch, MembershipExecutionEpoch: v.MembershipExecutionEpoch, RemovalEpoch: v.RemovalEpoch, CanExecute: v.CanExecute, OwnerEmail: v.OwnerEmail, TenantName: v.TenantName}, nil
}

func (a importExecutionAuthority) Agent(ctx context.Context, actor, tenant, agent string, execute bool) (execution.Scope, error) {
	v, err := (managed.RuntimeAuthority{Directory: a.directory}).Authorize(ctx, actor, tenant, agent, execute)
	if err != nil {
		return execution.Scope{}, err
	}
	owner, err := a.Owner(ctx, actor, tenant, v.UserID, execute)
	if err != nil {
		return execution.Scope{}, err
	}
	if owner.FleetID != v.FleetID || owner.ActorAuthorizationEpoch != v.ActorAuthorizationEpoch || owner.MembershipExecutionEpoch != v.MembershipExecutionEpoch {
		return execution.Scope{}, execprotocol.ErrDenied
	}
	return execution.Scope{OwnerScope: owner, AgentID: v.AgentID, AgentExecutionEpoch: v.AgentExecutionEpoch, Capabilities: v.Capabilities}, nil
}
