package runtimeconfig

import (
	"errors"
	"time"

	"github.com/Euphie/llm-proxy/internal/modeldirectory"
	"github.com/Euphie/llm-proxy/internal/profile"
)

var (
	ErrNotFound             = errors.New("Profile runtime state not found")
	ErrRevisionConflict     = errors.New("Profile runtime revision conflict")
	ErrRollbackIncompatible = errors.New("routing Policy rollback is incompatible with current model state")
	ErrModelConflict        = errors.New("Profile model already exists")
	ErrModelTransition      = errors.New("Profile model state transition is invalid")
	ErrModelStillInUse      = errors.New("Profile model is still referenced by the active runtime")
)

type ModelMutationKind string

const (
	ModelAdd     ModelMutationKind = "model_add"
	ModelUpdate  ModelMutationKind = "model_update"
	ModelRestore ModelMutationKind = "model_restore"
	ModelRetire  ModelMutationKind = "model_retire"
)

type ModelMutationInput struct {
	ProfileID        int64
	ExpectedRevision int64
	Kind             ModelMutationKind
	ModelID          string
	CapabilityJSON   []byte
	Reason           string
	Actor            string
}

type EmergencyOfflineInput struct {
	ProfileID        int64
	ExpectedRevision int64
	ModelID          string
	Policy           profile.RoutingPolicyConfig
	ProfileConfig    *profile.Config
	Reason           string
	Actor            string
}

type ProfileMutationInput struct {
	ProfileID        int64
	ExpectedRevision int64
	Profile          profile.SaveInput
	MakeDefault      bool
	Reason           string
	Actor            string
}

type ProfileCreateInput struct {
	Profile     profile.SaveInput
	Models      []modeldirectory.Record
	Policy      *profile.RoutingPolicyConfig
	MakeDefault bool
	Reason      string
	Actor       string
}

type ChangeKind string

const (
	ChangeMigration        ChangeKind = "migration"
	ChangeApply            ChangeKind = "apply"
	ChangeRollback         ChangeKind = "rollback"
	ChangeEmergencyOffline ChangeKind = "emergency_offline"
)

type State struct {
	ProfileID             int64     `json:"profile_id"`
	Revision              int64     `json:"revision"`
	ActivePolicyVersionID int64     `json:"active_policy_version_id"`
	ModelCatalogRevision  int64     `json:"model_catalog_revision"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type PolicyVersion struct {
	ID              int64                       `json:"id"`
	ProfileID       int64                       `json:"profile_id"`
	Sequence        int64                       `json:"policy_sequence"`
	Policy          profile.RoutingPolicyConfig `json:"policy"`
	SourceVersionID *int64                      `json:"source_version_id,omitempty"`
	Kind            ChangeKind                  `json:"change_kind"`
	Reason          string                      `json:"change_reason"`
	Actor           string                      `json:"created_by"`
	CreatedAt       time.Time                   `json:"created_at"`
}

type ApplyPolicyInput struct {
	ProfileID                 int64
	ExpectedRevision          int64
	Policy                    profile.RoutingPolicyConfig
	SourceVersionID           int64
	Kind                      ChangeKind
	Reason                    string
	Actor                     string
	ResetOrdinarySessionLocks bool
}

type ApplyPolicyResult struct {
	State  State         `json:"runtime_state"`
	Active PolicyVersion `json:"active"`
}

type Aggregate struct {
	Profile profile.Record          `json:"profile"`
	Models  []modeldirectory.Record `json:"models"`
	Active  *PolicyVersion          `json:"active,omitempty"`
	State   State                   `json:"runtime_state"`
}
