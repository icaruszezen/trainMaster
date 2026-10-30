package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
)

// Project API: /api/v1/projects, and PUT /api/v1/datasets/{id}/projects.
// The JSON shapes are defined by the types in internal/project; this file
// maps HTTP onto project.Service.

func (s *server) projectRoutes(r chi.Router) {
	r.Get("/", s.listProjects)
	r.Post("/", s.createProject)
	r.Route("/{projectID}", func(r chi.Router) {
		r.Get("/", s.getProject)
		r.Patch("/", s.updateProject)
		r.Delete("/", s.deleteProject)
		r.Get("/labels", s.listProjectLabels)
		r.Get("/datasets", s.listProjectDatasets)
		r.Post("/datasets", s.addProjectDatasets)
		r.Delete("/datasets/{datasetID}", s.removeProjectDataset)
	})
}

// projectError writes the response for an error returned by
// project.Service (or dataset.Service, for the dataset lists).
func (s *server) projectError(w http.ResponseWriter, r *http.Request, err error) {
	var pe *project.Error
	if errors.As(err, &pe) {
		status, code := projectErrorStatus(pe.Kind)
		writeError(w, status, code, pe.Message)
		return
	}
	s.datasetError(w, r, err)
}

func projectErrorStatus(kind error) (int, string) {
	switch kind {
	case project.ErrNotFound:
		return http.StatusNotFound, codeNotFound
	case project.ErrInvalid:
		return http.StatusUnprocessableEntity, codeValidationFailed
	case project.ErrConflict:
		return http.StatusConflict, codeConflict
	}
	return http.StatusInternalServerError, codeInternal
}

// projectID parses {projectID}. It answers 404 for invalid IDs.
func projectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(r, "projectID")
	if !ok {
		notFoundMessage(w, "项目不存在")
	}
	return id, ok
}

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) {
	items, err := s.projects.ListProjects(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

type createProjectRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	DatasetIDs  []int64 `json:"dataset_ids"`
}

func (s *server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.projects.CreateProject(r.Context(), project.CreateProjectInput{
		Name: req.Name, Description: req.Description, DatasetIDs: req.DatasetIDs,
	})
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) getProject(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	p, err := s.projects.GetProject(r.Context(), id)
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type updateProjectRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (s *server) updateProject(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var req updateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.projects.UpdateProject(r.Context(), id, project.UpdateProjectInput{
		Name: req.Name, Description: req.Description,
	})
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	if err := s.projects.DeleteProject(r.Context(), id); err != nil {
		s.projectError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listProjectLabels(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	items, err := s.projects.ListLabels(r.Context(), id)
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

// listProjectDatasets is GET /api/v1/datasets?project_id={id}, except that
// it answers 404 for a project that does not exist.
func (s *server) listProjectDatasets(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	if err := s.projects.Exists(r.Context(), id); err != nil {
		s.projectError(w, r, err)
		return
	}
	items, err := s.datasets.ListDatasets(r.Context(), dataset.DatasetQuery{ProjectID: id})
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}

type projectDatasetsRequest struct {
	DatasetIDs []int64 `json:"dataset_ids"`
}

func (s *server) addProjectDatasets(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var req projectDatasetsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.projects.AddDatasets(r.Context(), id, req.DatasetIDs)
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) removeProjectDataset(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	datasetID, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	if err := s.projects.RemoveDataset(r.Context(), id, datasetID); err != nil {
		s.projectError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type datasetProjectsRequest struct {
	ProjectIDs []int64 `json:"project_ids"`
}

// setDatasetProjects replaces the projects of a dataset and returns the
// updated dataset.
func (s *server) setDatasetProjects(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	var req datasetProjectsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ProjectIDs == nil {
		// Leaving the field out must not silently remove every project.
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少 project_ids")
		return
	}
	if err := s.projects.SetDatasetProjects(r.Context(), id, req.ProjectIDs); err != nil {
		s.projectError(w, r, err)
		return
	}
	d, err := s.datasets.GetDataset(r.Context(), id)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
