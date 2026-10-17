package service

import "time"

type TTFTProtectionModelView struct {
	Executions               []TTFTProtectionExecutionView `json:"executions"`
	RuntimeID                string                        `json:"runtime_id"`
	AlgorithmVersion         string                        `json:"algorithm_version"`
	PolicyGeneration         uint64                        `json:"policy_generation"`
	TriggerTTFTMs            int                           `json:"trigger_ttft_ms"`
	TriggerThresholdMs       int64                         `json:"trigger_threshold_ms"`
	TriggeredAt              time.Time                     `json:"triggered_at"`
	BadEvidenceExpiresAt     time.Time                     `json:"bad_evidence_expires_at"`
	RecoverySessionCount     int                           `json:"recovery_session_count"`
	RecoverySessionsRequired int                           `json:"recovery_sessions_required"`
	RecoveryOrdinarySamples  int                           `json:"recovery_ordinary_samples"`
	RecoveryTrialSamples     int                           `json:"recovery_trial_samples"`
	RecoveryReason           string                        `json:"recovery_reason"`
	LastRecoveryBlockReason  string                        `json:"last_recovery_block_reason,omitempty"`
	LastRecoveryBlockAt      time.Time                     `json:"last_recovery_block_at,omitempty"`
	RevalidationEpoch        uint64                        `json:"revalidation_epoch"`
	Model                    string                        `json:"model"`
	State                    string                        `json:"state"`
	EWMAms                   float64                       `json:"ewma_ms"`
	LastTTFTMs               int64                         `json:"last_ttft_ms"`
	SampleCount              uint64                        `json:"sample_count"`
	LastSampleAt             time.Time                     `json:"last_sample_at"`
	ExpiresAt                time.Time                     `json:"expires_at"`
	Reason                   string                        `json:"reason,omitempty"`
	RecoverySamples          int                           `json:"recovery_samples"`
	RecoverySamplesRequired  int                           `json:"recovery_samples_required"`
}

func fillTTFTProtectionEvidence(view *TTFTProtectionModelView, e *openAITTFTGuardEntry) {
	if view == nil || e == nil {
		return
	}
	view.RuntimeID = e.runtimeID
	view.AlgorithmVersion = openAITTFTGuardAlgorithmVersion
	view.PolicyGeneration = e.policyGeneration
	view.TriggerTTFTMs = e.triggerTTFTMs
	view.TriggerThresholdMs = e.triggerThresholdMs
	view.TriggeredAt = e.degradedAt
	view.BadEvidenceExpiresAt = e.badExpiresAt
	view.RecoverySessionCount = e.recoverySessions
	view.RecoverySessionsRequired = 2
	view.RecoveryOrdinarySamples = e.recoveryOrdinarySamples
	view.RecoveryTrialSamples = e.recoveryTrialSamples
	view.RecoveryReason = e.recoveryReason
	view.LastRecoveryBlockReason = e.lastRecoveryBlockReason
	view.LastRecoveryBlockAt = e.lastRecoveryBlockAt
	view.RevalidationEpoch = e.recoveryEpoch
}
