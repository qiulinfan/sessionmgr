package cryptox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

func EnsureIdentity(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		identity, err := LoadIdentity(path)
		if err != nil {
			return "", err
		}
		return identity.Recipient().String(), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	content := fmt.Sprintf("# created by sessionmgr\n# public key: %s\n%s\n", identity.Recipient(), identity)
	if err := atomicWrite(path, []byte(content)); err != nil {
		return "", err
	}
	return identity.Recipient().String(), nil
}

func LoadIdentity(path string) (*age.X25519Identity, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		identity, err := age.ParseX25519Identity(line)
		if err != nil {
			return nil, err
		}
		return identity, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no age identity found in %s", path)
}

func ParseRecipients(values []string) ([]age.Recipient, error) {
	result := make([]age.Recipient, 0, len(values))
	for _, value := range values {
		recipient, err := age.ParseX25519Recipient(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("age recipient %q: %w", value, err)
		}
		result = append(result, recipient)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one age recipient is required")
	}
	return result, nil
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
