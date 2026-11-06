package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/recycle"
)

// The recycle bin (回收站, package recycle): the datasets, review versions
// and models deleted in the last recycle.RetentionDays days. Deleting one
// of them (DELETE on its resource) moves it here; it can be restored until
// it is purged for good. There is no way to purge it sooner.
func (s *server) recycleBinRoutes(r chi.Router) {
	r.Get("/", s.listRecycleBin)
	r.Post("/datasets/{datasetID}/restore", s.restoreDataset)
	r.Post("/versions/{versionID}/restore", s.restoreVersion)
	if s.models != nil {
		r.Post("/models/{modelID}/restore", s.restoreModel)
	}
}

// recycleBin is the body of GET /api/v1/recycle-bin. Each list is the most
// recently deleted first.
type recycleBin struct {
	RetentionDays int                      `json:"retention_days"`
	Datasets      []dataset.TrashedDataset `json:"datasets"`
	Versions      []dataset.TrashedVersion `json:"versions"`
	Models        []model.TrashedModel     `json:"models"` // empty without the model repository
}

func (s *server) listRecycleBin(w http.ResponseWriter, r *http.Request) {
	bin := recycleBin{RetentionDays: recycle.RetentionDays, Models: []model.TrashedModel{}}
	var err error
	if bin.Datasets, err = s.datasets.ListTrashedDatasets(r.Context()); err != nil {
		s.datasetError(w, r, err)
		return
	}
	if bin.Versions, err = s.datasets.ListTrashedVersions(r.Context()); err != nil {
		s.datasetError(w, r, err)
		return
	}
	if s.models != nil {
		if bin.Models, err = s.models.ListTrashedModels(r.Context()); err != nil {
			s.modelError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, bin)
}

type restoreDatasetRequest struct {
	// Name restores the dataset under another name (409 name_taken).
	Name *string `json:"name"`
}

func (s *server) restoreDataset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIDOr404(w, r, "datasetID", "回收站中没有这个数据集")
	if !ok {
		return
	}
	var req restoreDatasetRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	d, err := s.datasets.RestoreDataset(r.Context(), id, dataset.RestoreInput{Name: req.Name})
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) restoreVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIDOr404(w, r, "versionID", "回收站中没有这个版本")
	if !ok {
		return
	}
	v, err := s.datasets.RestoreVersion(r.Context(), id)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type restoreModelRequest struct {
	// ProjectName restores the model's project, deleted with it, under
	// another name (409 name_taken).
	ProjectName *string `json:"project_name"`
}

func (s *server) restoreModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIDOr404(w, r, "modelID", "回收站中没有这个模型")
	if !ok {
		return
	}
	var req restoreModelRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	m, err := s.models.RestoreModel(r.Context(), id, model.RestoreInput{ProjectName: req.ProjectName})
	if err != nil {
		var pe *project.Error
		if errors.As(err, &pe) {
			s.projectError(w, r, err)
			return
		}
		s.modelError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}
