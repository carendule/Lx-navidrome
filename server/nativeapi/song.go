package nativeapi

import (
	"errors"
	"net/http"

	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/core"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils/req"
)

// deleteSongs removes the underlying files from disk and the corresponding media_file
// records. Emptied albums/artists and orphaned playlist entries are cleaned up
// automatically by core.Maintenance.DeleteSongs.
func deleteSongs(maintenance core.Maintenance) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		p := req.Params(r)
		ids := p.Strings("id")
		if len(ids) == 0 {
			http.Error(w, "missing id parameter", http.StatusBadRequest)
			return
		}

		err := maintenance.DeleteSongs(ctx, ids)
		if errors.Is(err, rest.ErrPermissionDenied) {
			http.Error(w, "Access denied: admin privileges required", http.StatusForbidden)
			return
		}
		if errors.Is(err, model.ErrNotFound) {
			http.Error(w, "song not found", http.StatusNotFound)
			return
		}
		if err != nil {
			log.Error(ctx, "Error deleting songs", "ids", ids, err)
			http.Error(w, "failed to delete songs", http.StatusInternalServerError)
			return
		}

		writeDeleteManyResponse(w, r, ids)
	}
}
