package strategy

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestOverridePatchDiffApplyAndSingleFieldRestore(t *testing.T) {
	generated := strategyTestConfig().AutoRouting.Strategy
	edited := cloneStrategyConfigForTest(generated)
	edited.Alias = "人工别名"
	edited.Routes[0].Candidates[0].QualityScoreBPS = 9_700

	patch, err := DiffOverrides(generated, edited)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"/alias", "/routes/0/candidates/0/quality_score_bps"}
	if got := overridePaths(patch); !reflect.DeepEqual(got, wantPaths) {
		t.Fatalf("paths=%v want=%v patch=%+v", got, wantPaths, patch)
	}

	refreshed := cloneStrategyConfigForTest(generated)
	refreshed.Alias = "新推荐别名"
	refreshed.Routes[0].Candidates[0].QualityScoreBPS = 8_800
	refreshed.Routes[0].Candidates[0].StabilityScoreBPS = 9_100
	applied, err := ApplyOverrides(refreshed, patch)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Alias != "人工别名" || applied.Routes[0].Candidates[0].QualityScoreBPS != 9_700 ||
		applied.Routes[0].Candidates[0].StabilityScoreBPS != 9_100 {
		t.Fatalf("applied=%+v", applied)
	}

	patch = RemoveOverride(patch, "/routes/0/candidates/0/quality_score_bps")
	applied, err = ApplyOverrides(refreshed, patch)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Alias != "人工别名" || applied.Routes[0].Candidates[0].QualityScoreBPS != 8_800 {
		t.Fatalf("restored=%+v patch=%+v", applied, patch)
	}
}

func TestOverridePatchTreatsStructuralArrayChangesAsWholeArray(t *testing.T) {
	generated := strategyTestConfig().AutoRouting.Strategy
	edited := cloneStrategyConfigForTest(generated)
	edited.Routes = append(edited.Routes, edited.Routes[0])
	edited.Routes[1].ID = "extra"

	patch, err := DiffOverrides(generated, edited)
	if err != nil {
		t.Fatal(err)
	}
	if got := overridePaths(patch); !reflect.DeepEqual(got, []string{"/routes"}) {
		t.Fatalf("structural paths=%v patch=%+v", got, patch)
	}
	applied, err := ApplyOverrides(generated, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, edited) {
		t.Fatalf("applied=%+v edited=%+v", applied, edited)
	}
}

func TestOverridePatchRepresentsRemovalAndEscapesJSONPointer(t *testing.T) {
	before := map[string]any{"a/b": map[string]any{"~key": "value"}}
	after := map[string]any{"a/b": map[string]any{}}
	patch := diffJSON(before, after, "")
	if len(patch) != 1 || patch[0].Path != "/a~1b/~0key" || !patch[0].Remove {
		t.Fatalf("patch=%+v", patch)
	}
	applied, err := applyJSON(before, patch)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a/b": map[string]any{}}
	if !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied=%#v want=%#v", applied, want)
	}
}

func TestOverridePatchJSONRoundTrip(t *testing.T) {
	patch := OverridePatch{{Path: "/alias", Value: json.RawMessage(`"manual"`)}}
	contents, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	var decoded OverridePatch
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, patch) {
		t.Fatalf("decoded=%+v patch=%+v", decoded, patch)
	}
}

func overridePaths(patch OverridePatch) []string {
	paths := make([]string, 0, len(patch))
	for _, override := range patch {
		paths = append(paths, override.Path)
	}
	return paths
}

func cloneStrategyConfigForTest(config profile.RoutingStrategyConfig) profile.RoutingStrategyConfig {
	contents, _ := json.Marshal(config)
	var cloned profile.RoutingStrategyConfig
	_ = json.Unmarshal(contents, &cloned)
	return cloned
}
