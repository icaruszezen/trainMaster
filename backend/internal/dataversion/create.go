package dataversion

import "context"

// OWNER: backend-core agent. Validating inputs, previews, creating,
// retrying, editing the note and deleting versions (dispatching to the
// recycle bin for ready versions).

// Preview counts what a version created with in would contain, from the
// datasets' index (no files are read). in.Note is ignored.
func (s *Service) Preview(ctx context.Context, in CreateInput) (Preview, error) {
	panic("dataversion: Preview not implemented")
}

// Create validates in, numbers the version and queues its build.
func (s *Service) Create(ctx context.Context, in CreateInput) (DataVersion, error) {
	panic("dataversion: Create not implemented")
}

// Retry queues a failed version again, with the same configuration.
func (s *Service) Retry(ctx context.Context, id int64) (DataVersion, error) {
	panic("dataversion: Retry not implemented")
}

// Update changes the note of a ready or failed version.
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (DataVersion, error) {
	panic("dataversion: Update not implemented")
}

// Delete moves a ready version to the recycle bin (moveToBin) and removes
// a failed or queued one for good; a building one is canceled first.
func (s *Service) Delete(ctx context.Context, id int64) error {
	panic("dataversion: Delete not implemented")
}

// DeleteProjectVersions prepares the deletion of a project: its ready
// versions go to the recycle bin; failed, queued and building ones are
// removed (a build is canceled), each like Delete. It never answers a
// conflict.
func (s *Service) DeleteProjectVersions(ctx context.Context, projectID int64) error {
	panic("dataversion: DeleteProjectVersions not implemented")
}
