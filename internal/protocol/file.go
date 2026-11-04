package protocol

import "time"

// File request and result bodies (spec section 6.6).
type FileReadRequest struct {
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	Limit    int64  `json:"limit"`
	Encoding string `json:"encoding"`
}

type FileReadResult struct {
	Content string    `json:"content"`
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	Mtime   time.Time `json:"mtime"`
}

type FileWriteRequest struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
	Mkdir     bool   `json:"mkdir"`
	SHA256    string `json:"sha256"`
}

type FilePathRequest struct {
	Path string `json:"path"`
}

type FileListRequest struct {
	Path       string `json:"path"`
	Recursive  bool   `json:"recursive"`
	MaxEntries *int   `json:"max_entries"`
}

type FileRemoveRequest struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

type FileMoveRequest struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"`
}

type FileDownloadRequest struct {
	URL      string            `json:"url"`
	Path     string            `json:"path"`
	MaxBytes *int64            `json:"max_bytes"`
	Retries  *int              `json:"retries"`
	Headers  map[string]string `json:"headers"`
}
