package admin

import (
	"net/http"
)

type sessionResponse struct {
	Username                   string `json:"username"`
	MustChangePassword         bool   `json:"must_change_password"`
	Initialized                bool   `json:"initialized"`
	InitializationState        string `json:"initialization_state"`
	HighRiskDefaultCredentials bool   `json:"high_risk_default_credentials"`
	Warning                    string `json:"warning,omitempty"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

const bootstrapCredentialWarning = "High risk: the default admin/admin credentials are active. Change the password immediately."

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	principal, session, err := a.auth.Login(
		r.Context(),
		r.RemoteAddr,
		request.Username,
		request.Password,
	)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	response, err := a.sessionStatus(r, principal)
	if err != nil {
		_ = a.auth.Logout(r.Context(), session.Token)
		a.writeInternalError(w, r, err)
		return
	}
	SetAuthCookies(w, r, session)
	writeJSON(w, http.StatusOK, response)
}

func (a *API) getSession(w http.ResponseWriter, r *http.Request) {
	principal, err := principalForRequest(r)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	response, err := a.sessionStatus(r, principal)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var request passwordRequest
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	principal, err := principalForRequest(r)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	if err := a.auth.accounts.ChangePassword(
		r.Context(),
		principal.AccountID,
		request.CurrentPassword,
		request.NewPassword,
	); err != nil {
		a.writeDomainError(w, r, err)
		return
	}
	if err := a.activateProfiles(r.Context()); err != nil {
		a.logger.ErrorContext(
			r.Context(),
			"admin password changed but Profile activation failed",
			"method", r.Method,
			"path", r.URL.Path,
			"error", err,
		)
		closeCookies(w, r)
		writeError(
			w,
			http.StatusServiceUnavailable,
			"runtime_sync_failed",
			"Password changed, but Profile activation failed. Sign in again after the service recovers.",
			nil,
		)
		return
	}

	account, err := a.auth.accounts.Get(r.Context())
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	principal = principalFromAccount(account)
	response, err := a.sessionStatus(r, principal)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	session, err := a.auth.sessions.Create(r.Context(), account.AuthVersion, sessionTTL)
	if err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	SetAuthCookies(w, r, session)
	writeJSON(w, http.StatusOK, response)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if err := decodeJSON(w, r, &request); err != nil {
		a.writeRequestError(w, r, err)
		return
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		a.writeDomainError(w, r, ErrUnauthorized)
		return
	}
	if err := a.auth.Logout(r.Context(), cookie.Value); err != nil {
		a.writeInternalError(w, r, err)
		return
	}
	closeCookies(w, r)
	writeOK(w)
}

func (a *API) sessionStatus(
	r *http.Request,
	principal Principal,
) (sessionResponse, error) {
	initialized, state, err := a.initializationFor(r.Context(), principal)
	if err != nil {
		return sessionResponse{}, err
	}
	response := sessionResponse{
		Username:                   principal.Username,
		MustChangePassword:         principal.MustChangePassword,
		Initialized:                initialized,
		InitializationState:        state,
		HighRiskDefaultCredentials: principal.MustChangePassword,
	}
	if principal.MustChangePassword {
		response.Warning = bootstrapCredentialWarning
	}
	return response, nil
}
