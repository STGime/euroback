package storage

import (
	"mime"
	"path"
	"strings"
)

// Serving content types (#697 follow-up). Objects uploaded without a type
// (console bulk uploads, scripts) come back from S3 as "" or
// application/octet-stream, which some players (Safari / iOS audio) refuse.
// The gateway fills in a type — but only a passive one: an end user
// controls both a file's bytes and its name, so an inferred text/html or
// image/svg+xml would run script on the project's API origin. Active types
// are always served as attachments.

// genericContentType: no useful type recorded.
func genericContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return ct == "" || ct == "application/octet-stream" || ct == "binary/octet-stream"
}

// extensionTypes covers common media the Go table (and a bare container
// without /etc/mime.types) lacks.
var extensionTypes = map[string]string{
	".m4a": "audio/mp4", ".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg",
	".oga": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac", ".aac": "audio/aac",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".ico": "image/x-icon",
	".pdf": "application/pdf", ".txt": "text/plain; charset=utf-8", ".csv": "text/csv; charset=utf-8",
}

// passiveContentType reports whether a browser renders ct without running
// script (images other than SVG, audio, video, PDF, plain text / CSV).
func passiveContentType(ct string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	switch {
	case base == "image/svg+xml":
		return false
	case strings.HasPrefix(base, "image/"), strings.HasPrefix(base, "audio/"), strings.HasPrefix(base, "video/"):
		return true
	case base == "application/pdf", base == "text/plain", base == "text/csv":
		return true
	}
	return false
}

// activeContentType: types a browser may execute or render as a document.
func activeContentType(ct string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	switch base {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml",
		"text/javascript", "application/javascript", "application/ecmascript", "text/ecmascript":
		return true
	}
	return false
}

// resolveContentType picks the Content-Type to serve: the stored S3 type if
// it's specific; else the type recorded at upload (recorded), else one
// inferred from the key's extension — the fallbacks only if passive.
func resolveContentType(key, s3Type, recorded string) string {
	if !genericContentType(s3Type) {
		return s3Type
	}
	if !genericContentType(recorded) && passiveContentType(recorded) {
		return recorded
	}
	ext := strings.ToLower(path.Ext(key))
	if ct, ok := extensionTypes[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" && passiveContentType(ct) {
		return ct
	}
	return "application/octet-stream"
}
