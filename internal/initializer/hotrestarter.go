package initializer

import (
	"os"
	"path/filepath"

	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-client/pkg/template"
)

// PlaceHotRestarter writes the Python wrapper to /var/lib/elchi/hotrestarter/hotrestarter.py if it does not exist.
func (i *Initializer) PlaceHotRestarter() error {
	path := filepath.Join(models.ElchiLibPath, "hotrestarter", "hotrestarter.py")
	if _, err := os.Stat(path); err == nil {
		i.Logger.Info("hotrestarter.py already exists, skipping")
		return nil
	} else if !os.IsNotExist(err) {
		i.Logger.Errorf("failed to check hotrestarter.py: %v", err)
		return err
	}

	err := os.WriteFile(path, []byte(template.PythonWrapper), 0755)
	if err != nil {
		i.Logger.Errorf("failed to write hotrestarter.py: %v", err)
		return err
	}
	return nil
}
