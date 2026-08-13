package rolesourcedr

import (
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/storage"
)

func StorageFromEnv() storage.Storage {
	if cloud := storage.NewS3StorageFromEnv(); cloud != nil {
		return cloud
	}
	if strings.TrimSpace(os.Getenv("LOCAL_UPLOAD_DIR")) == "" {
		return nil
	}
	return storage.NewLocalStorageFromEnv()
}
