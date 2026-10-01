package managementhttp

import (
	"context"
	"mime"
	"net/http"
	"strconv"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

type ArtifactAPI interface {
	BeginArtifact(context.Context, string, string, string, execution.ArtifactRequest) (execution.ArtifactUpload, error)
	WriteArtifact(context.Context, string, string, string, string, execprotocol.FileChunk) (execution.ArtifactUpload, error)
	CommitArtifact(context.Context, string, string, string, string) (execution.Artifact, error)
	Artifact(context.Context, string, string, string, string) (execution.Artifact, error)
	Artifacts(context.Context, string, string, string, string, int) ([]execution.Artifact, error)
	ReadArtifact(context.Context, string, string, string, string, int64, int) (execprotocol.FileChunk, error)
	DeleteArtifact(context.Context, string, string, string, string) error
}

func (s *Server) beginArtifact(w http.ResponseWriter, r *http.Request, user management.User) {
	var body execution.ArtifactRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.BeginArtifact(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), body)
	respond(w, v, err)
}

func (s *Server) writeArtifact(w http.ResponseWriter, r *http.Request, user management.User) {
	var body execprotocol.FileChunk
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.WriteArtifact(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("artifact"), body)
	respond(w, v, err)
}

func (s *Server) commitArtifact(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct{}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.CommitArtifact(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("artifact"))
	respond(w, v, err)
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.Artifact(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("artifact"))
	respond(w, v, err)
}

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.Artifacts(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.URL.Query().Get("after"), 100)
	respond(w, v, err)
}

func (s *Server) readArtifact(w http.ResponseWriter, r *http.Request, user management.User) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		respond(w, nil, execprotocol.ErrInvalid)
		return
	}
	v, err := s.options.Execution.ReadArtifact(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("artifact"), offset, execprotocol.FileChunkBytes)
	respond(w, v, err)
}

func (s *Server) deleteArtifact(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct{}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, nil, s.options.Execution.DeleteArtifact(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("artifact")))
}

func (s *Server) downloadArtifact(w http.ResponseWriter, r *http.Request, user management.User) {
	api := s.options.Execution
	tenant, agent, id := r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("artifact")
	artifact, err := api.Artifact(r.Context(), user.ID, tenant, agent, id)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if artifact.State != "ready" {
		respond(w, nil, execprotocol.ErrConflict)
		return
	}
	chunk, err := api.ReadArtifact(r.Context(), user.ID, tenant, agent, id, 0, execprotocol.FileChunkBytes)
	if err != nil {
		respond(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Request.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.Request.Manifest.Size, 10))
	for {
		if chunk.Validate() != nil || chunk.Offset+int64(len(chunk.Data)) > artifact.Request.Manifest.Size {
			panic(http.ErrAbortHandler)
		}
		if _, err := w.Write(chunk.Data); err != nil {
			return
		}
		offset := chunk.Offset + int64(len(chunk.Data))
		if offset == artifact.Request.Manifest.Size {
			return
		}
		if len(chunk.Data) == 0 {
			panic(http.ErrAbortHandler)
		}
		chunk, err = api.ReadArtifact(r.Context(), user.ID, tenant, agent, id, offset, execprotocol.FileChunkBytes)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}
