// Package recycle holds the rules of the recycle bin (回收站) shared by
// the modules whose deletes go through it: datasets, the review versions
// of datasets and models. Deleting one of them moves it to the bin (a soft
// delete, see migration 0012_recycle_bin.sql); it stays there for
// Retention, during which it can be restored, and is then purged for good
// by the background cleanup of its module. Items cannot be purged sooner.
package recycle

import "time"

// Retention is how long a deleted item stays in the recycle bin.
const Retention = 7 * 24 * time.Hour

// RetentionDays is Retention in days, as shown to users.
const RetentionDays = int(Retention / (24 * time.Hour))

// Kinds of items in the recycle bin (the kind field of the API).
const (
	KindDataset        = "dataset"
	KindDatasetVersion = "dataset_version"
	KindModel          = "model"
)

// PurgeAt returns when an item deleted at deletedAt is due to be purged.
// The cleanup that purges it runs every 30 minutes (datasets) or every
// hour (models), so it goes at most that much later.
func PurgeAt(deletedAt time.Time) time.Time { return deletedAt.Add(Retention) }

// RetentionSeconds is Retention for SQL: rows with
// deleted_at <= now() - RetentionSeconds * interval '1 second' are due.
const RetentionSeconds = int64(Retention / time.Second)
