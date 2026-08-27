package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/agenttrajectory"
	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestAgentTrajectoryRoutesListDecryptAndDeleteWithoutReevaluation(t *testing.T) {
	fixture := newTestAPI(t)
	cookies, csrf := fixture.changePassword(t)
	profile := fixture.createProfile(t, cookies, csrf, "agent-audit", true)
	secret := "secret-that-must-not-leak"
	payload, err := fixture.trajectoryCipher.Seal(agenttrajectory.Plaintext{Events: []agenttrajectory.Event{
		{Role: "user", Kind: agenttrajectory.EventUserMessage, Text: "inspect deployment"},
		{Role: "assistant", Kind: agenttrajectory.EventToolCall, CallID: "c1", ToolName: "deploy", Arguments: json.RawMessage(`{"password":"` + secret + `"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.trajectories.Create(context.Background(), agenttrajectory.CreateInput{
		ProfileID: profile.ID, ProfileSlug: profile.Slug, Protocol: routing.OperationAnthropicMessages,
		Source: agenttrajectory.SourceRequestSnapshot, Strategy: "active", Route: "agent",
		TaskType: "tool_use", Difficulty: "medium", Risk: "normal", VisionMode: "none",
		ModelPath: []string{"model-a"}, ToolCalls: 1, Turns: 1, Payload: payload,
		StartedAt: fixture.now, ExpiresAt: fixture.now.Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	completedAt := fixture.now
	if err := fixture.trajectories.Complete(context.Background(), record.ID, agenttrajectory.ContentUpdate{
		ModelPath: record.ModelPath, ToolCalls: 1, Turns: 1, Payload: payload, CompletedAt: &completedAt,
	}); err != nil {
		t.Fatal(err)
	}

	unauthenticated := fixture.request(t, http.MethodGet, "/_admin/api/agent-trajectories", "", nil, "")
	assertAPIError(t, unauthenticated, http.StatusUnauthorized, "unauthorized")
	listed := fixture.request(t, http.MethodGet,
		"/_admin/api/agent-trajectories?profile_id="+strconv.FormatInt(profile.ID, 10)+"&page=1&page_size=25",
		"", cookies, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var page agentTrajectoryPage
	decodeTestJSON(t, listed, &page)
	if page.Total != 1 || page.TotalPages != 1 || len(page.Items) != 1 ||
		page.Summary.Total != 1 || page.Summary.Completed != 1 {
		t.Fatalf("page=%+v", page)
	}
	for _, forbidden := range []string{secret, "ciphertext", "nonce", "session_key", "authorization", "credential"} {
		if strings.Contains(strings.ToLower(listed.Body.String()), strings.ToLower(forbidden)) {
			t.Fatalf("list leaked %q: %s", forbidden, listed.Body.String())
		}
	}

	detailPath := "/_admin/api/agent-trajectories/" + strconv.FormatInt(record.ID, 10)
	detail := fixture.request(t, http.MethodGet, detailPath, "", cookies, "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "inspect deployment") ||
		!strings.Contains(detail.Body.String(), "[REDACTED]") || strings.Contains(detail.Body.String(), secret) {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	invalid := fixture.request(t, http.MethodGet, "/_admin/api/agent-trajectories?status=rerun", "", cookies, "")
	assertAPIError(t, invalid, http.StatusBadRequest, "invalid_request")

	withoutCSRF := fixture.request(t, http.MethodDelete, detailPath, `{}`, cookies, "")
	assertAPIError(t, withoutCSRF, http.StatusForbidden, "csrf_failed")
	deleted := fixture.request(t, http.MethodDelete, detailPath, `{}`, cookies, csrf)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := fixture.request(t, http.MethodGet, detailPath, "", cookies, "")
	assertAPIError(t, missing, http.StatusNotFound, "agent_trajectory_not_found")

	unsupportedReevaluation := fixture.request(t, http.MethodPost, detailPath+"/reevaluate", `{}`, cookies, csrf)
	assertAPIError(t, unsupportedReevaluation, http.StatusNotFound, "not_found")
}
