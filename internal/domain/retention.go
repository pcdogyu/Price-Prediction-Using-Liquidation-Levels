package domain

import "time"

// Retention knobs shared by the daily maintenance window. Per-feature windows
// live with their own feature constants; these describe the on-disk leftovers
// that would otherwise grow without bound.
const (
	// BookSnapshotRetentionDays keeps raw order-book depth long enough for
	// short-term wall analysis without allowing the high-volume table to grow
	// indefinitely.
	BookSnapshotRetentionDays = 3
	// WallEventRetentionDays keeps the derived wall lifecycle history compact;
	// the full depth snapshots have a separate retention window above.
	WallEventRetentionDays = 1
	// CoinGlassCaptureRetention bounds the raw capture files kept on disk.
	CoinGlassCaptureRetention = 72 * time.Hour
	// DatabaseCompactMinimumFreeBytes is how much freed space retention must
	// accumulate before the maintenance window rewrites the database to reclaim it.
	DatabaseCompactMinimumFreeBytes int64 = 512 << 20
)
