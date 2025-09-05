package files

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
)

type DeleteFilesResult struct {
	DeletedFiles []string
	Errors       []error
}

func DeleteFiles(filePaths []string, log *logger.Logger) DeleteFilesResult {
	result := DeleteFilesResult{
		DeletedFiles: []string{},
		Errors:       []error{},
	}

	for _, path := range filePaths {
		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) {
				log.Warnf("Failed to remove file %s: %v", path, err)
				result.Errors = append(result.Errors, err)
			}
		} else {
			log.Debugf("Successfully deleted file: %s", path)
			result.DeletedFiles = append(result.DeletedFiles, path)
		}
	}

	return result
}

func DeleteServiceFiles(name string, port uint32, ifaceName string, log *logger.Logger) DeleteFilesResult {
	filename := name + "-" + fmt.Sprintf("%d", port)
	serviceName := filename + ".service"

	files := []string{
		filepath.Join(models.BootstrapsPath, filename+".yaml"),
		filepath.Join(models.NetplanPath, "90-"+ifaceName+".yaml"),
		filepath.Join(models.SystemdPath, serviceName),
		filepath.Join(models.SystemdRootPath, "journald@elchi-"+filename+".conf"),
	}

	return DeleteFiles(files, log)
}
