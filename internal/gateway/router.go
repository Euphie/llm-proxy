package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type router struct {
	registry *Registry
}

func NewRouter(registry *Registry) http.Handler {
	return &router{registry: registry}
}

func (r *router) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	escapedPath, querySuffix := splitRequestTarget(request)
	escapedSegment, escapedRemainder, hasSegment := removeFirstSegment(escapedPath)
	segment, segmentValid := decodeSegment(escapedSegment, hasSegment)

	if segmentValid && segment == "_admin" {
		http.NotFound(w, request)
		return
	}
	if segmentValid && segment == "v1" {
		r.serveDefault(w, request)
		return
	}
	r.serveNamed(w, request, segment, escapedRemainder, querySuffix, segmentValid)
}

func (r *router) serveDefault(w http.ResponseWriter, request *http.Request) {
	current := r.current()
	if current == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_not_configured", "Proxy not configured")
		return
	}
	item := current.byID[current.defaultID]
	if item == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_not_configured", "Proxy not configured")
		return
	}
	serveRuntime(w, request, item)
}

func (r *router) serveNamed(
	w http.ResponseWriter,
	request *http.Request,
	slug string,
	escapedRemainder string,
	querySuffix string,
	segmentValid bool,
) {
	current := r.current()
	if current == nil || current.byID[current.defaultID] == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_not_configured", "Proxy not configured")
		return
	}
	if !segmentValid {
		writeError(w, http.StatusNotFound, "profile_not_found", "Profile not found")
		return
	}
	item := current.bySlug[slug]
	if item == nil {
		writeError(w, http.StatusNotFound, "profile_not_found", "Profile not found")
		return
	}

	remainingPath, err := url.PathUnescape(escapedRemainder)
	if err != nil {
		writeError(w, http.StatusNotFound, "profile_not_found", "Profile not found")
		return
	}
	routed := request.Clone(request.Context())
	routed.URL.Path = remainingPath
	if (&url.URL{Path: remainingPath}).EscapedPath() == escapedRemainder {
		routed.URL.RawPath = ""
	} else {
		routed.URL.RawPath = escapedRemainder
	}
	routed.RequestURI = escapedRemainder + querySuffix
	serveRuntime(w, routed, item)
}

func (r *router) current() *snapshot {
	if r.registry == nil {
		return nil
	}
	return r.registry.current.Load()
}

func serveRuntime(w http.ResponseWriter, request *http.Request, item *runtime) {
	if !item.record.Enabled {
		writeError(w, http.StatusServiceUnavailable, "profile_disabled", "Profile disabled")
		return
	}
	if item.handler == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_not_configured", "Proxy not configured")
		return
	}
	item.handler.ServeHTTP(w, request)
}

func removeFirstSegment(path string) (slug, remaining string, ok bool) {
	if len(path) < 2 || path[0] != '/' {
		return "", "", false
	}
	separator := strings.IndexByte(path[1:], '/')
	if separator < 0 {
		return path[1:], "/", path[1:] != ""
	}
	separator++
	if separator == 1 {
		return "", "", false
	}
	return path[1:separator], path[separator:], true
}

func splitRequestTarget(request *http.Request) (path, querySuffix string) {
	requestURI := request.RequestURI
	if requestURI == "" {
		requestURI = request.URL.RequestURI()
	}
	path, query, hasQuery := strings.Cut(requestURI, "?")
	if hasQuery {
		return path, "?" + query
	}
	return path, ""
}

func decodeSegment(escaped string, present bool) (string, bool) {
	if !present {
		return "", false
	}
	segment, err := url.PathUnescape(escaped)
	if err != nil || strings.Contains(segment, "/") {
		return "", false
	}
	return segment, true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{
			Code:    code,
			Message: message,
		},
	})
}
