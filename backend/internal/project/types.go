package project

import "time"

// The JSON field names of the types in this file are the API contract with
// the frontend (frontend/src/features/projects/api.ts); do not rename them.

// Project groups the datasets of one inspection task. Its counters are
// totals over those datasets, computed when the project is read.
type Project struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	DatasetCount   int       `json:"dataset_count"`
	ImageCount     int       `json:"image_count"`
	AnnotatedCount int       `json:"annotated_count"` // VOC file with >= 1 object
	EmptyCount     int       `json:"empty_count"`     // VOC file with 0 objects
	InvalidCount   int       `json:"invalid_count"`   // VOC file that cannot be parsed
	ObjectCount    int       `json:"object_count"`
	TotalBytes     int64     `json:"total_bytes"`
	Covers         []Cover   `json:"covers"` // up to 4 images of its datasets for the card mosaic
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"` // also changes when datasets are added or removed
}

// Cover is an image of one of the project's datasets, with the version of
// its file for cache-busting thumbnail URLs.
type Cover struct {
	DatasetID int64  `json:"dataset_id"`
	ID        int64  `json:"id"`
	Version   string `json:"version"`
}

// Label is one label of a project's datasets with its totals.
type Label struct {
	Name         string `json:"name"`
	ObjectCount  int    `json:"object_count"`
	ImageCount   int    `json:"image_count"`
	DatasetCount int    `json:"dataset_count"` // datasets with at least one object of the label
}
