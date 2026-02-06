package envoy

import "github.com/CloudNativeWorks/elchi-client/internal/operations/common"

// ArchiveResponse represents the full response from archive API
type ArchiveResponse struct {
	Releases []common.ArchiveRelease `json:"releases"`
}

// Constants
const (
	DefaultBaseDir  = "/var/lib/elchi/envoys"
	ArchiveURL      = "https://archive.elchi.io/index.json"
	DefaultArch     = "linux-amd64"
	DownloadTimeout = 300 // 5 minutes
)
