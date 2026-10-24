package archive

import (
	"net/url"
	"path/filepath"
	"strings"
)

// readOnlySQLiteURI names an absolute database path as a read-only SQLite URI.
// A Windows drive path such as C:/x must follow the empty authority with a
// slash: file://C:/x would make C: the URI authority, which SQLite rejects.
func readOnlySQLiteURI(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed, RawQuery: "mode=ro"}).String()
}
