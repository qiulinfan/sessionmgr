package archive

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	attachmentStatusArchived        = "archived"
	attachmentStatusGitTracked      = "git_tracked"
	attachmentStatusTooLarge        = "too_large"
	attachmentStatusBusy            = "busy"
	attachmentStatusUnavailable     = "unavailable"
	attachmentStatusRemoteReference = "remote_reference"
	attachmentStatusSensitive       = "blocked_sensitive"
)

var (
	errAttachmentTooLarge = errors.New("attachment exceeds 50 MiB")
	commitPattern         = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

func prepareSessionAttachments(ctx context.Context, session Session, repo Repository) Session {
	sequence := 0
	for messageIndex := range session.Messages {
		for attachmentIndex := range session.Messages[messageIndex].Attachments {
			sequence++
			attachment := &session.Messages[messageIndex].Attachments[attachmentIndex]
			attachment.MessageIndex = messageIndex + 1
			attachment.AttachmentIndex = attachmentIndex + 1
			attachment.Name, _ = redact(attachmentBaseName(attachment.Name))
			resolveAttachment(ctx, session, repo, attachment)
			attachment.Name = readableAttachmentName(attachment.Name, attachment.MIMEType, sequence)
			if attachment.Status == attachmentStatusArchived {
				attachment.ArchivePath = filepath.ToSlash(filepath.Join(
					"attachments",
					semanticComponent(fmt.Sprintf("%03d-%s", sequence, attachment.Name), fmt.Sprintf("%03d-attachment", sequence)),
				))
			}
		}
	}
	return session
}

func resolveAttachment(ctx context.Context, session Session, repo Repository, attachment *Attachment) {
	var (
		data      []byte
		mediaType string
		err       error
	)
	switch attachment.SourceKind {
	case "embedded_data":
		data, mediaType, attachment.Size, err = decodeDataURL(attachment.SourceValue)
	case "local_path":
		data, attachment.Size, err = readStableAttachment(ctx, attachment.SourceValue)
	case "remote_reference":
		attachment.Status = attachmentStatusRemoteReference
		return
	default:
		attachment.Status = attachmentStatusUnavailable
		return
	}
	if errors.Is(err, errAttachmentTooLarge) {
		attachment.Status = attachmentStatusTooLarge
		return
	}
	if err != nil {
		if sourceErrorIsBusy(err) {
			attachment.Status = attachmentStatusBusy
		} else {
			attachment.Status = attachmentStatusUnavailable
		}
		return
	}
	attachment.MIMEType = normalizedAttachmentMIME(attachment.MIMEType, mediaType, attachment.Name, data)
	contentHash := digestBytes(data)
	if (attachment.ExpectedHash != "" && attachment.ExpectedHash != contentHash) ||
		(attachment.ExpectedSize > 0 && attachment.ExpectedSize != int64(len(data))) {
		attachment.Status = attachmentStatusUnavailable
		return
	}
	if sensitiveAttachment(attachment.Name, attachment.MIMEType, data) {
		attachment.Status = attachmentStatusSensitive
		attachment.Data = nil
		return
	}
	attachment.Size = int64(len(data))
	attachment.ContentHash = contentHash
	if repositoryPath, ok := gitTrackedAttachment(ctx, session, repo, attachment.LocalPath, data); ok {
		attachment.Status = attachmentStatusGitTracked
		attachment.RepositoryPath = repositoryPath
		attachment.GitCommit = session.Commit
		attachment.Data = nil
		return
	}
	attachment.Status = attachmentStatusArchived
	attachment.Data = data
}

func decodeDataURL(value string) ([]byte, string, int64, error) {
	if !strings.HasPrefix(strings.ToLower(value), "data:") {
		return nil, "", 0, fmt.Errorf("unsupported embedded attachment encoding")
	}
	header, encoded, found := strings.Cut(value[5:], ",")
	if !found {
		return nil, "", 0, fmt.Errorf("invalid data URL")
	}
	base64Encoded := false
	headerParts := strings.Split(header, ";")
	filtered := make([]string, 0, len(headerParts))
	for _, part := range headerParts {
		if strings.EqualFold(part, "base64") {
			base64Encoded = true
			continue
		}
		filtered = append(filtered, part)
	}
	mediaType := ""
	if len(filtered) > 0 && filtered[0] != "" {
		mediaType, _, _ = mime.ParseMediaType(strings.Join(filtered, ";"))
	}
	if base64Encoded {
		maximumEncoded := base64.StdEncoding.EncodedLen(int(MaxAttachmentBytes))
		if len(encoded) > maximumEncoded+4 {
			return nil, mediaType, int64(base64.StdEncoding.DecodedLen(len(encoded))), errAttachmentTooLarge
		}
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
		data, err := io.ReadAll(io.LimitReader(decoder, MaxAttachmentBytes+1))
		if err != nil {
			return nil, mediaType, 0, fmt.Errorf("decode attachment data URL: %w", err)
		}
		if int64(len(data)) > MaxAttachmentBytes {
			return nil, mediaType, int64(len(data)), errAttachmentTooLarge
		}
		return data, mediaType, int64(len(data)), nil
	}
	if int64(len(encoded)) > 3*(MaxAttachmentBytes+1) {
		return nil, mediaType, int64(len(encoded) / 3), errAttachmentTooLarge
	}
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		return nil, mediaType, 0, fmt.Errorf("decode attachment data URL: %w", err)
	}
	if int64(len(decoded)) > MaxAttachmentBytes {
		return nil, mediaType, int64(len(decoded)), errAttachmentTooLarge
	}
	return []byte(decoded), mediaType, int64(len(decoded)), nil
}

func readStableAttachment(ctx context.Context, path string) ([]byte, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	before, err := fingerprintSource(path)
	if err != nil {
		if sourceErrorIsBusy(err) {
			return nil, 0, fmt.Errorf("%w: attachment changed before read", errSourceBusy)
		}
		return nil, 0, err
	}
	if before.size > MaxAttachmentBytes {
		return nil, before.size, errAttachmentTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		if sourceErrorIsBusy(err) {
			return nil, 0, fmt.Errorf("%w: attachment could not be opened", errSourceBusy)
		}
		return nil, 0, err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return nil, 0, statErr
	}
	opened := sourceFingerprint{size: openedInfo.Size(), modTime: openedInfo.ModTime(), info: openedInfo}
	if !sameFingerprint(before, opened) {
		file.Close()
		return nil, 0, fmt.Errorf("%w: attachment was replaced while opening", errSourceBusy)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxAttachmentBytes+1))
	handleInfo, handleErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return nil, 0, readErr
	}
	if handleErr != nil {
		return nil, 0, handleErr
	}
	if closeErr != nil {
		return nil, 0, closeErr
	}
	if int64(len(data)) > MaxAttachmentBytes {
		return nil, int64(len(data)), errAttachmentTooLarge
	}
	after, err := fingerprintSource(path)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: attachment changed after read", errSourceBusy)
	}
	handleAfter := sourceFingerprint{size: handleInfo.Size(), modTime: handleInfo.ModTime(), info: handleInfo}
	if !sameFingerprint(before, handleAfter) || !sameFingerprint(before, after) {
		return nil, 0, fmt.Errorf("%w: attachment changed while being read", errSourceBusy)
	}
	return data, int64(len(data)), nil
}

func normalizedAttachmentMIME(current, embedded, name string, data []byte) string {
	for _, candidate := range []string{embedded, current, mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))} {
		candidate, _, _ = strings.Cut(strings.TrimSpace(candidate), ";")
		if candidate != "" && candidate != "application/octet-stream" && !strings.HasSuffix(candidate, "/*") {
			return strings.ToLower(candidate)
		}
	}
	detected, _, _ := strings.Cut(http.DetectContentType(data), ";")
	return strings.ToLower(strings.TrimSpace(detected))
}

func readableAttachmentName(name, mediaType string, sequence int) string {
	name = attachmentBaseName(name)
	if name == "" {
		name = "attachment-" + strconv.Itoa(sequence)
	}
	if filepath.Ext(name) == "" {
		name += extensionForMIME(mediaType)
	}
	return name
}

func extensionForMIME(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "audio/webm":
		return ".webm"
	case "audio/ogg":
		return ".ogg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		return ""
	}
}

func sensitiveAttachment(name, mediaType string, data []byte) bool {
	lowerName := strings.ToLower(strings.TrimSpace(name))
	sensitiveNames := map[string]bool{
		".env": true, "auth.json": true, "credentials.json": true,
		"auth.db": true, "credentials": true, "keychain.db": true, "secrets": true,
		"cookies": true, "cookies.sqlite": true, "login data": true,
		"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	}
	if sensitiveNames[lowerName] || strings.HasPrefix(lowerName, ".env.") {
		return true
	}
	if redactionPatterns[0].pattern.Match(data) {
		return true
	}
	if strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" {
		text := string(data)
		if secretAssignment.MatchString(text) {
			return true
		}
		for _, pattern := range redactionPatterns[1:] {
			if pattern.pattern.MatchString(text) {
				return true
			}
		}
	}
	return false
}

func gitTrackedAttachment(ctx context.Context, session Session, repo Repository, sourcePath string, data []byte) (string, bool) {
	if sourcePath == "" || session.CWD == "" || !commitPattern.MatchString(session.Commit) {
		return "", false
	}
	root, err := gitOutput(ctx, session.CWD, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	worktreeRepo, err := RepositoryFromPath(ctx, root)
	if err != nil || worktreeRepo.Key != repo.Key {
		return "", false
	}
	absolute := sourcePath
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(session.CWD, absolute)
	}
	canonicalRoot, rootErr := filepath.EvalSymlinks(root)
	canonicalSource, sourceErr := filepath.EvalSymlinks(filepath.Clean(absolute))
	if rootErr == nil && sourceErr == nil {
		root = canonicalRoot
		absolute = canonicalSource
	}
	relative, err := filepath.Rel(root, filepath.Clean(absolute))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	repositoryPath := filepath.ToSlash(relative)
	want, err := gitOutput(ctx, root, "rev-parse", "--verify", session.Commit+":"+repositoryPath)
	if err != nil {
		return "", false
	}
	command := exec.CommandContext(ctx, "git", "-C", root, "hash-object", "--stdin")
	command.Stdin = bytes.NewReader(data)
	actual, err := command.Output()
	if err != nil || strings.TrimSpace(string(actual)) != want {
		return "", false
	}
	return repositoryPath, true
}

func attachmentCounts(session Session) (total, archived int) {
	for _, message := range session.Messages {
		total += len(message.Attachments)
		for _, attachment := range message.Attachments {
			if attachment.Status == attachmentStatusArchived {
				archived++
			}
		}
	}
	return total, archived
}

func attachmentWarnings(session Session) []string {
	warnings := make([]string, 0)
	for _, message := range session.Messages {
		for _, attachment := range message.Attachments {
			var reason string
			switch attachment.Status {
			case attachmentStatusTooLarge:
				reason = "exceeds the 50 MiB per-file limit"
			case attachmentStatusBusy:
				reason = "was changing or busy and will be retried"
			case attachmentStatusUnavailable:
				reason = "was unavailable and will be retried"
			case attachmentStatusRemoteReference:
				reason = "is a remote reference and was not downloaded"
			case attachmentStatusSensitive:
				reason = "looked like credentials, environment secrets, or a private key and was not archived"
			}
			if reason != "" {
				warnings = append(warnings, fmt.Sprintf("attachment %q %s", attachment.Name, reason))
			}
		}
	}
	return warnings
}
