package httpapi

import (
	"net/http"

	"github.com/icaruszezen/trainmaster/backend/internal/project"
)

// Class list API: the class list of a project (/api/v1/projects/{id}/classes,
// see projectRoutes) and the classes a dataset's projects offer when
// annotating it (GET /api/v1/datasets/{id}/classes, see datasetRoutes).

// classListResponse is a project's class list: a List of classes with the
// version of the list, which a replacement names as its base_version.
type classListResponse struct {
	List[project.Class]
	Version string `json:"version"`
}

func newClassListResponse(l project.ClassList) classListResponse {
	return classListResponse{List: NewList(l.Classes, len(l.Classes)), Version: l.Version}
}

func (s *server) listProjectClasses(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	list, err := s.projects.ListClasses(r.Context(), id)
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newClassListResponse(list))
}

type setClassesRequest struct {
	Classes *[]project.Class `json:"classes"`
	// BaseVersion is the version of the list the edit started from;
	// without it the list is replaced whatever it is now.
	BaseVersion *string `json:"base_version"`
}

func (s *server) setProjectClasses(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var req setClassesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Classes == nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少 classes（类别列表，清空时为 []）")
		return
	}
	list, err := s.projects.SetClasses(r.Context(), id, *req.Classes, req.BaseVersion)
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newClassListResponse(list))
}

type addClassRequest struct {
	Name  *string `json:"name"`
	Color string  `json:"color"`
}

// addProjectClass appends a class to the list unless it has one of that
// name, without replacing the rest of the list.
func (s *server) addProjectClass(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var req addClassRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少 name（类别名称）")
		return
	}
	list, err := s.projects.AddClass(r.Context(), id, project.Class{Name: *req.Name, Color: req.Color})
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newClassListResponse(list))
}

// listDatasetClasses lists the classes of the dataset's projects, merged
// (project.Service.DatasetClasses). It has no version: the lists are
// edited per project.
func (s *server) listDatasetClasses(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	items, err := s.projects.DatasetClasses(r.Context(), id)
	if err != nil {
		s.projectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewList(items, len(items)))
}
