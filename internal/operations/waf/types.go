package waf

import "github.com/CloudNativeWorks/elchi-client/internal/operations/common"

// ArchiveResponse represents the full response from archive API
type ArchiveResponse struct {
	CorozaReleases []common.ArchiveRelease `json:"coroza_releases"`
}

// Constants
const (
	DefaultBaseDir  = "/var/lib/elchi/waf"
	ArchiveURL      = "https://archive.elchi.io/index.json"
	DefaultArch     = "wasm-amd64"
	DownloadTimeout = 300 // 5 minutes
)
