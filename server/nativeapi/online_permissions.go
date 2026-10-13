package nativeapi

import (
	"net/http"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
)

func onlinePermissionAllowed(user model.User, permission string) bool {
	if user.IsAdmin {
		return true
	}
	if !user.OnlineSearchEnabled {
		return false
	}
	switch permission {
	case "search":
		return true
	case "browser":
		return user.OnlineBrowserDownloadEnabled
	case "server":
		return user.OnlineServerDownloadEnabled
	case "sync":
		return user.OnlinePlaylistSyncEnabled
	default:
		return false
	}
}

func requireOnlinePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := request.UserFrom(r.Context())
			if !ok {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if !onlinePermissionAllowed(user, permission) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
