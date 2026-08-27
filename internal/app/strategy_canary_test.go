package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestCanaryHandlerUsesStableAuthenticatedCohorts(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions, err := routing.NewSessionStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer caller-secret")
	headers.Set(routing.SessionIDHeader, "agent-session-42")
	strategyID := int64(1)
	bucket := 9999
	for candidateID := int64(1); candidateID < 100 && bucket == 9999; candidateID++ {
		strategyID = candidateID
		var ok bool
		bucket, ok = sessions.CanaryBucket(headers, "agent-session-42", 8, strategyID)
		if !ok {
			t.Fatal("authenticated cohort was rejected")
		}
	}
	if bucket == 9999 {
		t.Fatal("could not build test cohort below the maximum canary percentage")
	}
	active := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "active")
	})
	canary := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "canary")
	})
	handler := canaryHandler(active, canary, sessions, 8, strategyID, bucket+1)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header = headers.Clone()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Body.String() != "canary" {
		t.Fatalf("identified response=%q", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set(routing.SessionIDHeader, "agent-session-42")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Body.String() != "active" {
		t.Fatalf("anonymous response=%q", response.Body.String())
	}
}

func TestCanaryHandlerStopsNewTrafficWhenSafetyCheckFails(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions, err := routing.NewSessionStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer caller-secret")
	headers.Set(routing.SessionIDHeader, "agent-session-42")
	strategyID := int64(1)
	bucket, ok := sessions.CanaryBucket(headers, "agent-session-42", 8, strategyID)
	if !ok {
		t.Fatal("authenticated cohort was rejected")
	}
	active := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "active") })
	canary := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "canary") })
	for _, check := range []func(context.Context) (bool, error){
		func(context.Context) (bool, error) { return true, nil },
		func(context.Context) (bool, error) { return false, errors.New("evidence unavailable") },
	} {
		handler := canaryHandler(active, canary, sessions, 8, strategyID, bucket+1, check)
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		request.Header = headers.Clone()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Body.String() != "active" {
			t.Fatalf("unsafe canary response=%q", response.Body.String())
		}
	}
}
