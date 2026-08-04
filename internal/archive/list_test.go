package archive

import "testing"

func TestNewerEntryParsesFractionalRFC3339(t *testing.T) {
	current := Entry{UpdatedAt: "2026-08-05T01:00:02Z", SnapshotHash: "sha256:a"}
	candidate := Entry{UpdatedAt: "2026-08-05T01:00:02.1Z", SnapshotHash: "sha256:b"}
	if !newerEntry(candidate, current) {
		t.Fatal("fractional timestamp should sort after the whole-second timestamp")
	}
}
