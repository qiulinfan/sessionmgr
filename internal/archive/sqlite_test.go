package archive

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadOnlySQLiteURIOpensDrivePathsWithSpecialCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		if got, want := readOnlySQLiteURI(`C:\Users\a b\x#y.db`), "file:///C:/Users/a%20b/x%23y.db?mode=ro"; got != want {
			t.Fatalf("drive path URI = %q, want %q", got, want)
		}
	}
	path := filepath.Join(t.TempDir(), "a b", "x#y.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec("CREATE TABLE threads(id TEXT); INSERT INTO threads VALUES('one')"); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	reader, err := sql.Open("sqlite", readOnlySQLiteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var id string
	if err = reader.QueryRow("SELECT id FROM threads").Scan(&id); err != nil || id != "one" {
		t.Fatalf("read-only open = %q, %v", id, err)
	}
	if _, err = reader.Exec("INSERT INTO threads VALUES('two')"); err == nil {
		t.Fatal("read-only URI accepted a write")
	}
}
