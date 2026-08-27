package modeldirectory

import (
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("profile model not found")

type Status string

const (
	StatusAvailable Status = "available"
	StatusOffline   Status = "offline"
	StatusRetired   Status = "retired"
)

type Record struct {
	ProfileID      int64           `json:"profile_id"`
	ModelID        string          `json:"model_id"`
	CapabilityJSON json.RawMessage `json:"capability"`
	Status         Status          `json:"status"`
	StatusReason   string          `json:"status_reason"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	RetiredAt      *time.Time      `json:"retired_at,omitempty"`
}

func (status Status) valid() bool {
	switch status {
	case StatusAvailable, StatusOffline, StatusRetired:
		return true
	default:
		return false
	}
}
