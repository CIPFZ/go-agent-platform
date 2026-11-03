package protocol

import "time"

// DisplayStatus is the display block of probe and capabilities (spec 6.1, 9).
type DisplayStatus struct {
	Ready        bool            `json:"ready"`
	Degraded     bool            `json:"degraded"`
	Count        int             `json:"count"`
	DPIAwareness string          `json:"dpi_awareness"`
	Primary      *PrimaryDisplay `json:"primary"`
}

// PrimaryDisplay is the primary monitor's physical resolution and scale.
type PrimaryDisplay struct {
	W     int     `json:"w"`
	H     int     `json:"h"`
	Scale float64 `json:"scale"`
}

// ProbeResult is the result of GET /v1/probe.
type ProbeResult struct {
	Version       string        `json:"version"`
	APIVersion    string        `json:"api_version"`
	Platform      string        `json:"platform"`
	StartedAt     time.Time     `json:"started_at"`
	UptimeSeconds int64         `json:"uptime_seconds"`
	LastInputAt   *time.Time    `json:"last_input_at"`
	Display       DisplayStatus `json:"display"`
	SessionLocked bool          `json:"session_locked"`
	SessionType   string        `json:"session_type"`
	UACPrompt     bool          `json:"uac_prompt"`
}

// SessionStatus is the session block of capabilities.
type SessionStatus struct {
	Locked    bool   `json:"locked"`
	Type      string `json:"type"`
	UACPrompt bool   `json:"uac_prompt"`
}

// Limits are the request limits of spec section 9.
type Limits struct {
	JSONBodyMaxBytes     int64 `json:"json_body_max_bytes"`
	MultipartMaxBytes    int64 `json:"multipart_max_bytes"`
	ExecOutputMaxBytes   int64 `json:"exec_output_max_bytes"`
	FileReadMaxBytes     int64 `json:"file_read_max_bytes"`
	DownloadMaxBytes     int64 `json:"download_max_bytes"`
	BatchMaxActions      int   `json:"batch_max_actions"`
	MaxConcurrency       int   `json:"max_concurrency"`
	SyncTimeoutMaxMS     int64 `json:"sync_timeout_max_ms"`
	TransferTimeoutMaxMS int64 `json:"transfer_timeout_max_ms"`
}

// CapabilitiesResult is the result of GET /v1/capabilities.
type CapabilitiesResult struct {
	Platform string        `json:"platform"`
	Version  string        `json:"version"`
	Display  DisplayStatus `json:"display"`
	Session  SessionStatus `json:"session"`
	Actions  []string      `json:"actions"`
	Limits   Limits        `json:"limits"`
}

// CaptureRequest is the body of POST /v1/screen/capture.
type CaptureRequest struct {
	Display  *int   `json:"display"`
	Region   *Rect  `json:"region"`
	Cursor   bool   `json:"cursor"`
	Format   string `json:"format"`
	Quality  int    `json:"quality"`
	Encoding string `json:"encoding"`
}

// CaptureResult is the base64-encoded result of POST /v1/screen/capture.
type CaptureResult struct {
	Image      string    `json:"image"`
	Format     string    `json:"format"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	Cursor     Point     `json:"cursor"`
	CapturedAt time.Time `json:"captured_at"`
	SHA256     string    `json:"sha256"`
}
