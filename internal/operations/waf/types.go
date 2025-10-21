package waf

import "time"

// ArchiveRelease represents a single coroza release from the archive API
type ArchiveRelease struct {
	Version  string          `json:"version"`
	Date     time.Time       `json:"date"`
	Binaries []ArchiveBinary `json:"binaries"`
}

// ArchiveBinary represents a WASM binary download info
type ArchiveBinary struct {
	Arch        string `json:"arch"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
}

// ArchiveResponse represents the full response from archive API
type ArchiveResponse struct {
	CorozaReleases []ArchiveRelease `json:"coroza_releases"`
}

// Constants
const (
	DefaultBaseDir  = "/var/lib/elchi/waf"
	ArchiveURL      = "https://archive.elchi.io/index.json"
	DefaultArch     = "wasm-amd64"
	DownloadTimeout = 300 // 5 minutes
)