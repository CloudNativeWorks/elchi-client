package envoy

import "time"

// ArchiveRelease represents a single release from the archive API
type ArchiveRelease struct {
	Version  string          `json:"version"`
	Date     time.Time       `json:"date"`
	Binaries []ArchiveBinary `json:"binaries"`
}

// ArchiveBinary represents a binary download info
type ArchiveBinary struct {
	Arch        string `json:"arch"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
}

// ArchiveResponse represents the full response from archive API
type ArchiveResponse struct {
	Releases []ArchiveRelease `json:"releases"`
}

// Constants
const (
	DefaultBaseDir  = "/var/lib/elchi/envoys"
	ArchiveURL      = "https://archive.elchi.io/index.json"
	DefaultArch     = "linux-amd64"
	DownloadTimeout = 300 // 5 minutes
)