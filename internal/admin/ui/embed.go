package adminui

import (
	"embed"
	"net/http"
	"strings"
)

const (
	assetPrefix        = "/_admin/assets/"
	currentAssetPrefix = assetPrefix + "current/"
	uiCSP              = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
)

//go:embed static/*.html static/*.css static/*.js static/*.svg
var assets embed.FS

func NewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if r.URL.Path == strings.TrimSuffix(assetPrefix, "/") ||
			r.URL.Path == strings.TrimSuffix(currentAssetPrefix, "/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, currentAssetPrefix) {
			serveAsset(w, strings.TrimPrefix(r.URL.Path, currentAssetPrefix))
			return
		}
		if strings.HasPrefix(r.URL.Path, assetPrefix) {
			serveAsset(w, strings.TrimPrefix(r.URL.Path, assetPrefix))
			return
		}
		serveIndex(w)
	})
}

func serveAsset(w http.ResponseWriter, name string) {
	var contentType string
	switch {
	case strings.HasSuffix(name, ".css"):
		contentType = "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		contentType = "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		contentType = "image/svg+xml"
	default:
		http.NotFound(w, nil)
		return
	}
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, nil)
		return
	}
	content, err := assets.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func serveIndex(w http.ResponseWriter) {
	content, err := assets.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "embedded UI unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", uiCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
