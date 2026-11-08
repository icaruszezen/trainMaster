package dataversion

import "context"

// OWNER: backend-integration agent. The recycle bin: moving ready versions
// in, restoring, listing and purging them; deleting a project's versions.

// moveToBin moves a ready version to the recycle bin (Delete calls it).
func (s *Service) moveToBin(ctx context.Context, id int64) error {
	panic("dataversion: moveToBin not implemented")
}

// Restore takes a version out of the recycle bin (with its project when the
// project went to the bin with it).
func (s *Service) Restore(ctx context.Context, id int64, in RestoreInput) (DataVersion, error) {
	panic("dataversion: Restore not implemented")
}

// ListTrashed returns the versions in the recycle bin, newest deletion
// first.
func (s *Service) ListTrashed(ctx context.Context) ([]TrashedDataVersion, error) {
	panic("dataversion: ListTrashed not implemented")
}

// janitorLoop purges expired versions and removes leftover folders, at
// start and every janitorInterval, until ctx ends.
func (s *Service) janitorLoop(ctx context.Context) {
	panic("dataversion: janitorLoop not implemented")
}
