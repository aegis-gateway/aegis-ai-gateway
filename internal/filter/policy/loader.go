package policy

import (
	"os"
	"path/filepath"
)

// LoadRegoFiles reads all .rego files from the given directory.
func LoadRegoFiles(dir string) (map[string]string, error) {
	modules := make(map[string]string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".rego" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		modules[entry.Name()] = string(data)
	}
	return modules, nil
}
