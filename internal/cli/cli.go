package cli

import (
	"path/filepath"

	"github.com/dector/gust/internal/config"
)

// Parse converts command-line arguments into a Gust configuration.
func Parse(args []string) (config.Config, error) {
	_ = args

	root, err := filepath.Abs(".")
	if err != nil {
		return config.Config{}, err
	}

	return config.Config{Root: root}, nil
}
