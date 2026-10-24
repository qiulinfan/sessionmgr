package archive

import (
	"errors"
	"os"
	"path/filepath"
)

// byteExactAttributes stops Git from converting line endings in the archive.
// Document hashes cover the exact bytes written, and a Windows checkout with
// core.autocrlf would otherwise turn every LF document into CRLF.
const byteExactAttributes = "* -text\n"

// ensureByteExactAttributes writes the archive's .gitattributes when it has
// none. An existing file belongs to the owner and is never changed.
func ensureByteExactAttributes(output string) error {
	path := filepath.Join(output, ".gitattributes")
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = file.WriteString(byteExactAttributes); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
