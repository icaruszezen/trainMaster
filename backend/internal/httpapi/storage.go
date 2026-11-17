package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
)

// Storage node API: /api/v1/storage. Manages local and remote storage nodes,
// trainer short-term dataset cache policies, and online cross-node migrations.

func (s *server) storageRoutes(r chi.Router) {
	r.Get("/nodes", s.listStorageNodes)
	r.Post("/nodes", s.createStorageNode)
	r.Post("/nodes/test", s.testStorageNode) // static segment before /{nodeID}
	r.Route("/nodes/{nodeID}", func(r chi.Router) {
		r.Patch("/", s.updateStorageNode)
		r.Post("/activate", s.activateStorageNode)
		r.Delete("/", s.deleteStorageNode)
	})

	r.Get("/cache-policy", s.getTrainerCachePolicy)
	r.Put("/cache-policy", s.updateTrainerCachePolicy)

	r.Get("/migratable", s.listMigratableItems)
	r.Get("/migrations", s.listStorageMigrations)
	r.Post("/migrations", s.createStorageMigrations)
	r.Post("/migrations/{migrationID}/cancel", s.cancelStorageMigration)
}

func writeStorageNodeError(w http.ResponseWriter, err error) bool {
	var se *storagenode.Error
	if !errors.As(err, &se) {
		return false
	}
	status, code := storageNodeErrorStatus(se.Kind)
	msg := se.Message
	if se.Kind == storagenode.ErrUnavailable && se.Cause != nil {
		msg = se.Error()
	}
	writeError(w, status, code, msg)
	return true
}

func storageNodeErrorStatus(kind error) (int, string) {
	switch {
	case errors.Is(kind, storagenode.ErrNotFound):
		return http.StatusNotFound, codeNotFound
	case errors.Is(kind, storagenode.ErrInvalid):
		return http.StatusUnprocessableEntity, codeValidationFailed
	case errors.Is(kind, storagenode.ErrConflict):
		return http.StatusConflict, codeConflict
	case errors.Is(kind, storagenode.ErrInsufficientStorage):
		return http.StatusInsufficientStorage, codeInsufficientStorage
	case errors.Is(kind, storagenode.ErrUnavailable):
		return http.StatusServiceUnavailable, codeStorageUnavailable
	default:
		return http.StatusInternalServerError, codeInternal
	}
}

func (s *server) storageError(w http.ResponseWriter, r *http.Request, err error) {
	if writeStorageNodeError(w, err) {
		return
	}
	s.unexpectedError(w, r, err)
}

func storageNodeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "nodeID", "存储节点不存在")
}

func storageMigrationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathIDOr404(w, r, "migrationID", "迁移任务不存在")
}

func (s *server) listStorageNodes(w http.ResponseWriter, r *http.Request) {
	items, err := s.storageNodes.ListNodes(r.Context())
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) createStorageNode(w http.ResponseWriter, r *http.Request) {
	var in storagenode.CreateNodeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	node, err := s.storageNodes.CreateNode(r.Context(), in)
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, node)
}

func (s *server) testStorageNode(w http.ResponseWriter, r *http.Request) {
	var in storagenode.TestNodeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	res, err := s.storageNodes.TestConnection(r.Context(), in)
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) updateStorageNode(w http.ResponseWriter, r *http.Request) {
	id, ok := storageNodeID(w, r)
	if !ok {
		return
	}
	var in storagenode.UpdateNodeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	node, err := s.storageNodes.UpdateNode(r.Context(), id, in)
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (s *server) activateStorageNode(w http.ResponseWriter, r *http.Request) {
	id, ok := storageNodeID(w, r)
	if !ok {
		return
	}
	node, err := s.storageNodes.ActivateNode(r.Context(), id)
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (s *server) deleteStorageNode(w http.ResponseWriter, r *http.Request) {
	id, ok := storageNodeID(w, r)
	if !ok {
		return
	}
	if err := s.storageNodes.DeleteNode(r.Context(), id); err != nil {
		s.storageError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getTrainerCachePolicy(w http.ResponseWriter, r *http.Request) {
	policy, err := s.storageNodes.GetTrainerCachePolicy(r.Context())
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *server) updateTrainerCachePolicy(w http.ResponseWriter, r *http.Request) {
	var in storagenode.TrainerCachePolicy
	if !decodeJSON(w, r, &in) {
		return
	}
	policy, err := s.storageNodes.UpdateTrainerCachePolicy(r.Context(), in)
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *server) listMigratableItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.storageNodes.ListMigratableItems(r.Context())
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) listStorageMigrations(w http.ResponseWriter, r *http.Request) {
	items, err := s.storageNodes.ListMigrations(r.Context())
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

func (s *server) createStorageMigrations(w http.ResponseWriter, r *http.Request) {
	var in storagenode.CreateMigrationsInput
	if !decodeJSON(w, r, &in) {
		return
	}
	items, err := s.storageNodes.CreateMigrations(r.Context(), in)
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, NewList(items, len(items)))
}

func (s *server) cancelStorageMigration(w http.ResponseWriter, r *http.Request) {
	id, ok := storageMigrationID(w, r)
	if !ok {
		return
	}
	item, err := s.storageNodes.CancelMigration(r.Context(), id)
	if err != nil {
		s.storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
