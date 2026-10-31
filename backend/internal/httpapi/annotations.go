package httpapi

import (
	"net/http"
	"strconv"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Annotation API, below /api/v1/datasets/{datasetID} (see datasetRoutes):
// stepping through images, saving an image's boxes to its VOC file, and
// the dataset's class list.

// maxAnnotationBody bounds the body of an annotation save: 10,000 boxes
// with long labels stay well below it.
const maxAnnotationBody = 8 << 20

func (s *server) getImageNeighbors(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := datasetIDs(w, r, "imageID")
	if !ok {
		return
	}
	q, ok := imageFilters(w, r)
	if !ok {
		return
	}
	n, err := s.datasets.ImageNeighbors(r.Context(), id, imageID, q)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type saveAnnotationRequest struct {
	Objects     *[]dataset.AnnotationObject `json:"objects"`
	BaseVersion *string                     `json:"base_version"`
	Force       bool                        `json:"force"`
}

func (s *server) saveImageAnnotation(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := datasetIDs(w, r, "imageID")
	if !ok {
		return
	}
	var req saveAnnotationRequest
	if !decodeJSONLimit(w, r, &req, maxAnnotationBody) {
		return
	}
	switch {
	case req.Objects == nil:
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少 objects（目标列表，没有目标时为 []）")
		return
	case req.BaseVersion == nil && !req.Force:
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少 base_version（开始编辑时的标注版本，没有标注时为空字符串）")
		return
	}
	in := dataset.SaveAnnotationInput{Objects: *req.Objects, Force: req.Force}
	if req.BaseVersion != nil {
		in.BaseVersion = *req.BaseVersion
	}
	img, err := s.datasets.SaveAnnotation(r.Context(), id, imageID, in)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

func (s *server) deleteImageAnnotation(w http.ResponseWriter, r *http.Request) {
	id, imageID, ok := datasetIDs(w, r, "imageID")
	if !ok {
		return
	}
	query := r.URL.Query()
	force := false
	if v := query.Get("force"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(w, "参数 force 无效，应为 true 或 false")
			return
		}
		force = b
	}
	if !query.Has("base_version") && !force {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "缺少参数 base_version（开始编辑时的标注版本）")
		return
	}
	img, err := s.datasets.DeleteAnnotation(r.Context(), id, imageID, query.Get("base_version"), force)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

// classListResponse is a dataset's class list: a List of classes with the
// version of the list, which a replacement names as its base_version.
type classListResponse struct {
	List[dataset.Class]
	Version string `json:"version"`
}

func newClassListResponse(l dataset.ClassList) classListResponse {
	return classListResponse{List: NewList(l.Classes, len(l.Classes)), Version: l.Version}
}

func (s *server) listDatasetClasses(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
	if !ok {
		return
	}
	list, err := s.datasets.ListClasses(r.Context(), id)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newClassListResponse(list))
}

type setClassesRequest struct {
	Classes *[]dataset.Class `json:"classes"`
	// BaseVersion is the version of the list the edit started from;
	// without it the list is replaced whatever it is now.
	BaseVersion *string `json:"base_version"`
}

func (s *server) setDatasetClasses(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
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
	list, err := s.datasets.SetClasses(r.Context(), id, *req.Classes, req.BaseVersion)
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newClassListResponse(list))
}

type addClassRequest struct {
	Name  *string `json:"name"`
	Color string  `json:"color"`
}

// addDatasetClass appends a class to the list unless it has one of that
// name, without replacing the rest of the list.
func (s *server) addDatasetClass(w http.ResponseWriter, r *http.Request) {
	id, _, ok := datasetIDs(w, r, "")
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
	list, err := s.datasets.AddClass(r.Context(), id, dataset.Class{Name: *req.Name, Color: req.Color})
	if err != nil {
		s.datasetError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newClassListResponse(list))
}
