package aggregate

import (
	"errors"
	"strings"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestNormalizeGatewayInputRejectsDuplicatePublicModels(t *testing.T) {
	_, err := NormalizeGatewayInput(GatewayInput{
		Slug:        "team",
		DisplayName: "Team Gateway",
		Enabled:     true,
		Protocol:    profile.ProtocolOpenAI,
		Routes: []RouteInput{
			{
				PublicModel:       "gpt-4o",
				ProviderAccountID: 1,
				ProviderModel:     "provider-gpt-4o",
				Enabled:           true,
			},
			{
				PublicModel:       "gpt-4o",
				ProviderAccountID: 2,
				ProviderModel:     "other-gpt-4o",
				Enabled:           true,
			},
		},
	})
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), `duplicate public model "gpt-4o"`) {
		t.Fatalf("NormalizeGatewayInput() error = %v", err)
	}
}
