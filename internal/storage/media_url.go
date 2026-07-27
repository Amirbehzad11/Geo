package storage

import "strings"

// ResolvePublicMediaURL turns a Laravel public-disk relative path into an
// absolute URL. base should be Laravel APP_URL or APP_URL/storage.
// Examples:
//
//	base=http://host          path=vehicles/a.png -> http://host/storage/vehicles/a.png
//	base=http://host/storage  path=vehicles/a.png -> http://host/storage/vehicles/a.png
func ResolvePublicMediaURL(base, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		return path
	}
	if strings.HasPrefix(path, "/") {
		base = strings.TrimRight(strings.TrimSpace(base), "/")
		if base == "" {
			return path
		}
		// Absolute path on same host: join origin only.
		if i := strings.Index(base, "://"); i >= 0 {
			rest := base[i+3:]
			if slash := strings.Index(rest, "/"); slash >= 0 {
				return base[:i+3+slash] + path
			}
			return base + path
		}
		return path
	}

	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return path
	}
	path = strings.TrimLeft(path, "/")
	path = strings.TrimPrefix(path, "storage/")

	if strings.HasSuffix(base, "/storage") {
		return base + "/" + path
	}
	return base + "/storage/" + path
}
