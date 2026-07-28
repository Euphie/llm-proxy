package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatsBasicAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := statsBasicAuth("statspwd123456", next)

	tests := []struct {
		name     string
		username string
		password string
		want     int
	}{
		{name: "missing credentials", want: http.StatusUnauthorized},
		{name: "wrong username", username: "user", password: "statspwd123456", want: http.StatusUnauthorized},
		{name: "wrong password", username: "admin", password: "wrong", want: http.StatusUnauthorized},
		{name: "authorized", username: "admin", password: "statspwd123456", want: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/stats", nil)
			if tt.username != "" {
				request.SetBasicAuth(tt.username, tt.password)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != tt.want {
				t.Fatalf("status=%d, want %d", response.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized &&
				response.Header().Get("WWW-Authenticate") != `Basic realm="llm-proxy stats", charset="UTF-8"` {
				t.Fatal("wrong WWW-Authenticate header")
			}
		})
	}
}
