//go:build !windows

package archive

import "os"

func isPlatformBusyError(err error) bool {
	// Advisory Unix file locks do not make ordinary reads fail. Mutation is
	// detected by the shared stability window and post-read fingerprint check.
	return false
}

func renameOwnedFile(source, destination string) error {
	return os.Rename(source, destination)
}
