package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/Euphie/llm-proxy/internal/aggregate"
	"github.com/Euphie/llm-proxy/internal/profile"
)

type errorResponse struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields,omitempty"`
	} `json:"error"`
}

type requestError struct {
	status  int
	code    string
	message string
	fields  map[string]string
}

func (e *requestError) Error() string {
	return e.message
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &requestError{
			status:  http.StatusUnsupportedMediaType,
			code:    "unsupported_media_type",
			message: "Content-Type must be application/json.",
		}
	}
	if r.ContentLength > maxJSONBodyBytes {
		return &requestError{
			status:  http.StatusRequestEntityTooLarge,
			code:    "request_too_large",
			message: "JSON request body exceeds 1 MiB.",
		}
	}

	body := http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	defer body.Close()
	payload, err := io.ReadAll(body)
	if err != nil {
		return jsonDecodeError(err)
	}
	objectStart := 0
	for objectStart < len(payload) {
		value := payload[objectStart]
		if value != ' ' && value != '\t' && value != '\n' && value != '\r' {
			break
		}
		objectStart++
	}
	if objectStart == len(payload) || payload[objectStart] != '{' {
		return jsonDecodeError(errors.New("top-level JSON value is not an object"))
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return jsonDecodeError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return jsonDecodeError(err)
	}
	return nil
}

func jsonDecodeError(err error) error {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return &requestError{
			status:  http.StatusRequestEntityTooLarge,
			code:    "request_too_large",
			message: "JSON request body exceeds 1 MiB.",
		}
	}
	return &requestError{
		status:  http.StatusBadRequest,
		code:    "invalid_json",
		message: "Request body must be one valid JSON object with only supported fields.",
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(
	w http.ResponseWriter,
	status int,
	code, message string,
	fields map[string]string,
) {
	response := errorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	response.Error.Fields = fields
	writeJSON(w, status, response)
}

func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func (a *API) writeRequestError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *requestError
	if errors.As(err, &invalid) {
		writeError(w, invalid.status, invalid.code, invalid.message, invalid.fields)
		return
	}
	a.writeInternalError(w, r, err)
}

func (a *API) writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.", nil)
	case errors.Is(err, ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many login attempts. Try again later.", nil)
	case errors.Is(err, ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required.", nil)
	case errors.Is(err, ErrCSRF):
		writeError(w, http.StatusForbidden, "csrf_failed", "CSRF validation failed.", nil)
	case errors.Is(err, ErrPasswordChangeRequired):
		writeError(w, http.StatusForbidden, "password_change_required", "Change the default admin password before continuing.", nil)
	case errors.Is(err, ErrWeakPassword):
		writeError(
			w,
			http.StatusUnprocessableEntity,
			"validation_error",
			"Request validation failed.",
			map[string]string{"new_password": "Password must contain at least 10 characters."},
		)
	case errors.Is(err, profile.ErrNotFound):
		writeError(w, http.StatusNotFound, "profile_not_found", "Profile not found.", nil)
	case errors.Is(err, profile.ErrSlugConflict):
		writeError(w, http.StatusConflict, "profile_slug_conflict", "Profile slug already exists.", nil)
	case errors.Is(err, profile.ErrInvalidConfig),
		errors.Is(err, profile.ErrInvalidSlug),
		errors.Is(err, profile.ErrInvalidProtocol):
		writeError(
			w,
			http.StatusUnprocessableEntity,
			"validation_error",
			"Profile validation failed.",
			map[string]string{"profile": "Profile configuration is invalid."},
		)
	case errors.Is(err, profile.ErrDefaultRequired):
		writeError(w, http.StatusConflict, "default_profile_required", "An enabled default Profile is required.", nil)
	case errors.Is(err, aggregate.ErrNotFound):
		writeError(w, http.StatusNotFound, "aggregate_not_found", "Aggregate gateway resource not found.", nil)
	case errors.Is(err, aggregate.ErrProviderSlugConflict):
		writeError(w, http.StatusConflict, "provider_slug_conflict", "Provider slug already exists for this protocol.", nil)
	case errors.Is(err, aggregate.ErrGatewaySlugConflict):
		writeError(w, http.StatusConflict, "aggregate_slug_conflict", "Aggregate gateway slug already exists.", nil)
	case errors.Is(err, aggregate.ErrInvalidConfig):
		writeError(
			w,
			http.StatusUnprocessableEntity,
			"validation_error",
			"Aggregate gateway validation failed.",
			map[string]string{"aggregate": "Aggregate gateway configuration is invalid."},
		)
	default:
		a.writeInternalError(w, r, fmt.Errorf("handle admin API request: %w", err))
	}
}
