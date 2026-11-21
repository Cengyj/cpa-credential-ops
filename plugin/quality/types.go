// Package quality owns bounded evaluation jobs and conservative quality decisions.
package quality

import (
	"context"
	"fmt"
	"time"
)

const Version = "0.31.0"

const maxRuleModels = 50
const maxManualModels = 16

type HostCapabilities struct {
	CredentialManagement bool   `json:"credential_management"`
	ManagementSession    bool   `json:"management_session"`
	Mode                 string `json:"mode"`
	QualityPause         bool   `json:"quality_pause"`
	ManualRelease        bool   `json:"manual_release"`
	VerifiedEvidence     bool   `json:"verified_evidence"`
	ModelOperationCancel bool   `json:"model_operation_cancel"`
	StreamClose          bool   `json:"stream_close"`
}

func officialCapabilities() HostCapabilities {
	return HostCapabilities{Mode: "official_observation", StreamClose: true}
}

func unsupportedQuality() error {
	return fmt.Errorf("unsupported: official CPA does not provide independent quality pause or release")
}

func (en *Engine) Capabilities() HostCapabilities {
	if host, ok := en.host.(interface{ Capabilities() HostCapabilities }); ok {
		return host.Capabilities()
	}
	return officialCapabilities()
}

func (en *Engine) unsupportedRule(r *Rule) bool {
	return !en.Capabilities().QualityPause && (r.Action == "pause_credential" || r.AutomaticPause)
}

type Config struct {
	// Omitting the default keeps existing host configuration fingerprints stable.
	IncludeProviderTargets bool   `json:"include_provider_targets,omitempty" yaml:"include_provider_targets"`
	DataDir                string `json:"data_dir" yaml:"data_dir"`
	SchedulingEnabled      bool   `json:"scheduling_enabled" yaml:"scheduling_enabled"`
	AutomaticPause         bool   `json:"automatic_pause" yaml:"automatic_pause"`
	Workers                int    `json:"workers" yaml:"workers"`
	Queue                  int    `json:"queue" yaml:"queue"`
	MaxModels              int    `json:"max_models" yaml:"max_models"`
	MaxSamples             int    `json:"max_samples" yaml:"max_samples"`
	MaxOutputTokens        int    `json:"max_output_tokens" yaml:"max_output_tokens"`
	MaxResponseBytes       int    `json:"max_response_bytes" yaml:"max_response_bytes"`
	DailyCalls             int    `json:"daily_calls" yaml:"daily_calls"`
	DailyTokens            int64  `json:"daily_tokens" yaml:"daily_tokens"`
	RetentionDays          int    `json:"retention_days" yaml:"retention_days"`
	MaxStorageBytes        int64  `json:"max_storage_bytes" yaml:"max_storage_bytes"`
}

func DefaultConfig() Config {
	return Config{DataDir: "model-quality-data", Workers: 2, Queue: 32, MaxModels: 8, MaxSamples: 32, MaxOutputTokens: 4096, MaxResponseBytes: 262144, DailyCalls: 200, DailyTokens: 1000000, RetentionDays: 30, MaxStorageBytes: 128 << 20}
}
func (c Config) Validate() error {
	if c.DataDir == "" || c.Workers < 1 || c.Workers > 16 || c.Queue < 1 || c.Queue > 1024 || c.MaxModels < 1 || c.MaxModels > maxRuleModels || c.MaxSamples < 1 || c.MaxSamples > 128 || c.MaxOutputTokens < 16 || c.MaxOutputTokens > 32768 || c.MaxResponseBytes < 1024 || c.MaxResponseBytes > 2<<20 || c.DailyCalls < 1 || c.DailyTokens < 1 || c.RetentionDays < 1 || c.RetentionDays > 365 || c.MaxStorageBytes < 1<<20 {
		return fmt.Errorf("invalid resource limits")
	}
	return nil
}

type Parameters struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxTokens       int      `json:"max_tokens"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	Stream          bool     `json:"stream"`
}
type Judge struct {
	Mode            string `json:"mode"`
	AuthID          string `json:"auth_id,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	TemplateVersion string `json:"template_version"`
}
type RunConfig struct {
	ExpectedObjectType string     `json:"expected_object_type,omitempty"`
	ExpectedAuthIndex  string     `json:"expected_auth_index,omitempty"`
	ReviewStandard     string     `json:"review_standard,omitempty"`
	ControlAuthID      string     `json:"control_auth_id,omitempty"`
	ControlProvider    string     `json:"control_provider,omitempty"`
	AuthID             string     `json:"auth_id"`
	Provider           string     `json:"provider"`
	Models             []string   `json:"models"`
	QuestionID         string     `json:"question_id"`
	Prompt             string     `json:"prompt,omitempty"`
	Seed               int64      `json:"seed,omitempty"`
	Samples            int        `json:"samples"`
	Parameters         Parameters `json:"parameters"`
	Judge              Judge      `json:"judge"`
	IdempotencyKey     string     `json:"idempotency_key,omitempty"`
}
type Question struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Prompt  string `json:"prompt"`
	Answer  string `json:"answer,omitempty"`
	Seed    int64  `json:"seed,omitempty"`
	Family  string `json:"family,omitempty"`
}
type Frozen struct {
	RecoveryCheck            bool                `json:"recovery_check,omitempty"`
	ProviderName             string              `json:"provider_name,omitempty"`
	ProviderObservation      bool                `json:"provider_observation,omitempty"`
	ProviderDependencies     []string            `json:"provider_dependencies,omitempty"`
	ManualAccountFingerprint string              `json:"manual_account_fingerprint,omitempty"`
	ManualPolicyActionID     string              `json:"manual_policy_action_id,omitempty"`
	CatalogMissingModel      string              `json:"catalog_missing_model,omitempty"`
	UpstreamErrorActions     map[string]string   `json:"upstream_error_actions,omitempty"`
	ManagedProbe             *CredentialSnapshot `json:"managed_probe,omitempty"`
	ScopeBatchID             string              `json:"scope_batch_id,omitempty"`
	SubjectIndex             string              `json:"subject_auth_index,omitempty"`
	Scheduled                bool                `json:"scheduled_dispatch,omitempty"`
	JudgeIdentity            string              `json:"judge_identity,omitempty"`
	ControlIdentity          string              `json:"control_identity,omitempty"`
	ComparisonIdentity       string              `json:"comparison_identity,omitempty"`
	RunConfig
	Question             Question `json:"question"`
	IdentityVersion      string   `json:"identity_version"`
	Fingerprint          string   `json:"fingerprint"`
	RuleID               string   `json:"rule_id,omitempty"`
	RuleRevision         uint64   `json:"rule_revision,omitempty"`
	BatchID              string   `json:"batch_id,omitempty"`
	PayloadConfigVersion string   `json:"payload_config_version"`
}
type Usage struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
	Total  int64 `json:"total_tokens"`
}
type Evidence struct {
	PayloadConfigVersion string `json:"payload_config_version"`
	Verified             bool   `json:"verified"`
	Reason               string `json:"reason,omitempty"`
	RequestedModel       string `json:"requested_model"`
	ResolvedModel        string `json:"resolved_model"`
	AuthID               string `json:"auth_id"`
	PayloadFingerprint   string `json:"payload_fingerprint"`
	ConditionsChanged    bool   `json:"conditions_changed"`
	Path                 string `json:"path"`
}
type Sample struct {
	UpstreamCondition string   `json:"upstream_condition,omitempty"`
	AnswerMatch       string   `json:"answer_match,omitempty"`
	Model             string   `json:"model"`
	Index             int      `json:"index"`
	ExecutionStatus   string   `json:"execution_status"`
	QualityVerdict    string   `json:"quality_verdict"`
	Output            string   `json:"output"`
	Error             string   `json:"error,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	TotalMS           int64    `json:"total_ms"`
	TTFTMS            *int64   `json:"ttft_ms"`
	Usage             *Usage   `json:"usage"`
	JudgeUsage        *Usage   `json:"judge_usage"`
	ResponseModel     string   `json:"response_model,omitempty"`
	Comparable        bool     `json:"comparable"`
	Evidence          Evidence `json:"evidence"`
	Review            string   `json:"review,omitempty"`
	ReviewNote        string   `json:"review_note,omitempty"`
}
type SampleSlot struct {
	Model string `json:"model"`
	Index int    `json:"index"`
}
type AnswerPreview struct {
	SampleSlot
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated"`
}
type Run struct {
	HistoryCleared    bool             `json:"history_cleared,omitempty"`
	AnswerPreview     *AnswerPreview   `json:"answer_preview,omitempty"` // Derived on reads.
	PreflightDetail   *ScopeSkipDetail `json:"preflight_detail,omitempty"`
	SkipDetail        *ScopeSkipDetail `json:"skip_detail,omitempty"`
	UpstreamCondition string           `json:"upstream_condition,omitempty"`
	CredentialAction  string           `json:"credential_action,omitempty"`
	CredentialEmail   string           `json:"credential_email,omitempty"`
	PrunedSamples     int              `json:"pruned_samples,omitempty"`
	PrunedSlots       []SampleSlot     `json:"pruned_sample_slots,omitempty"`
	BlockReason       string           `json:"block_reason,omitempty"`
	Controls          []Sample         `json:"controls,omitempty"`
	ID                string           `json:"id"`
	Status            string           `json:"status"`
	Source            string           `json:"source"`
	CreatedAt         time.Time        `json:"created_at"`
	CompletedAt       *time.Time       `json:"completed_at,omitempty"`
	Config            Frozen           `json:"config"`
	Samples           []Sample         `json:"samples"`
	ActionStatus      string           `json:"action_status"`
	Error             string           `json:"error,omitempty"`
	CancelRequested   bool             `json:"cancel_requested"`
}
type Rule struct {
	ObjectTypes               []string          `json:"object_types,omitempty"`
	TargetGroup               string            `json:"target_group,omitempty"`
	TargetAuthIndex           string            `json:"target_auth_index,omitempty"`
	TargetObjectType          string            `json:"target_object_type,omitempty"`
	AuthorizedGroups          []string          `json:"authorized_groups,omitempty"`
	LegacyScope               bool              `json:"legacy_scope,omitempty"`
	LegacyProviderObservation bool              `json:"legacy_provider_observation,omitempty"`
	AdoptScope                bool              `json:"adopt_scope,omitempty"`
	UpstreamErrorActions      map[string]string `json:"upstream_error_actions,omitempty"`
	AutoRestore               bool              `json:"auto_restore,omitempty"`
	timerActive               bool
	timerWaitReason           string
	expectedTargetObjectType  string
	TargetScope               string                `json:"target_scope,omitempty"`
	LastScopeBatch            string                `json:"last_scope_batch,omitempty"`
	LastManualSkips           []string              `json:"last_manual_skips,omitempty"`
	LastScopeDispatch         *ScopeDispatchSummary `json:"last_scope_dispatch,omitempty"`
	PendingSchedule           *pendingRuleSchedule  `json:"pending_schedule,omitempty"`
	IntentRevision            uint64                `json:"intent_revision,omitempty"`
	Status                    string                `json:"status"`
	BlockedAuthID             string                `json:"blocked_auth_id,omitempty"`
	EvidenceSignature         string                `json:"evidence_signature,omitempty"`
	ID                        string                `json:"id"`
	Revision                  uint64                `json:"revision"`
	Name                      string                `json:"name"`
	RunConfig
	Action             string    `json:"action"`
	Enabled            bool      `json:"enabled"`
	SchedulingEnabled  bool      `json:"scheduling_enabled"`
	AutomaticPause     bool      `json:"automatic_pause"`
	IntervalSeconds    int       `json:"interval_seconds,omitempty"`
	Cron               string    `json:"cron,omitempty"`
	Timezone           string    `json:"timezone,omitempty"`
	FailThreshold      int       `json:"fail_threshold"`
	MaxResults         int       `json:"max_results,omitempty"`
	RetentionMode      string    `json:"retention_mode,omitempty"`
	RetentionDays      int       `json:"retention_days,omitempty"`
	FailCount          int       `json:"fail_count"`
	NextRun            time.Time `json:"next_run"`
	LastRun            string    `json:"last_run,omitempty"`
	ActionID           string    `json:"action_id,omitempty"`
	ActionRevision     uint64    `json:"action_revision,omitempty"`
	ActionRuleRevision uint64    `json:"action_rule_revision,omitempty"`
	IdentityVersion    string    `json:"identity_version,omitempty"`
}
type Target struct {
	providerProbeModels  map[string]bool
	providerFamilyName   string
	reasoningModelIDs    map[string]string
	ProviderGroupID      string `json:"provider_group_id,omitempty"`
	ProviderDisableUnit  string `json:"provider_disable_unit,omitempty"`
	ProviderProbeReason  string `json:"provider_probe_reason,omitempty"`
	ObjectType           string `json:"object_type,omitempty"`
	IsProvider           bool   `json:"is_provider,omitempty"`
	ProviderName         string `json:"provider_name,omitempty"`
	identityConflict     bool
	identityVerified     bool
	catalogVerified      bool
	Email                string           `json:"email,omitempty"`
	CredentialHandling   string           `json:"credential_handling,omitempty"`
	HandlingStateReason  string           `json:"handling_state_reason,omitempty"`
	ErrorPolicyBlock     *ScopeSkipDetail `json:"error_policy_block,omitempty"`
	RuntimeStatus        string           `json:"runtime_status,omitempty"`
	RetainedPause        bool             `json:"retained_pause,omitempty"`
	PayloadConfigVersion string           `json:"payload_config_version"`
	ComparisonIdentity   string           `json:"comparison_identity,omitempty"`
	AuthID               string           `json:"auth_id"`
	AuthIndex            string           `json:"auth_index"`
	Provider             string           `json:"provider"`
	Type                 string           `json:"type"`
	Models               []string         `json:"models"`
	Reasoning            any              `json:"reasoning"`
	IdentityVersion      string           `json:"identity_version"`
	ManualDisabled       bool             `json:"manual_disabled"`
	QualityPaused        bool             `json:"quality_paused"`
	EffectiveAvailable   bool             `json:"effective_available"`
	Reasons              []string         `json:"reasons"`
	CanPause             bool             `json:"can_pause"`
	CanRetest            bool             `json:"can_retest"`
	UnsupportedReason    string           `json:"unsupported_reason,omitempty"`
}
type Action struct {
	Status          string    `json:"status,omitempty"`
	AuthID          string    `json:"auth_id"`
	OwnerPlugin     string    `json:"owner_plugin"`
	RuleID          string    `json:"rule_id"`
	RuleRevision    uint64    `json:"rule_revision"`
	ActionID        string    `json:"action_id"`
	ActionRevision  uint64    `json:"action_revision"`
	EvidenceRunID   string    `json:"evidence_run_id"`
	IdentityVersion string    `json:"identity_version"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
}
type Execution struct {
	ManagedProbe                                    *CredentialSnapshot
	ResponseLimit                                   int
	AuthID, Provider, Model, Prompt, Source, System string
	Parameters                                      Parameters
	Judge                                           bool
}
type Host interface {
	Targets(context.Context) ([]Target, error)
	Actions(context.Context) ([]Action, error)
	Pause(context.Context, Action) (Action, error)
	Release(context.Context, Action) error
	Execute(context.Context, Execution) Sample
}
type Audit struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	RuleID   string    `json:"rule_id,omitempty"`
	RunID    string    `json:"run_id,omitempty"`
	ActionID string    `json:"action_id,omitempty"`
	Detail   string    `json:"detail"`
}
type Budget struct {
	Day            string `json:"day"`
	Calls          int    `json:"calls"`
	ReservedTokens int64  `json:"reserved_tokens"`
	KnownTokens    int64  `json:"known_tokens"`
	UnknownUsage   int    `json:"unknown_usage"`
}
type UsageEvent struct {
	TokensKnown bool      `json:"tokens_known"`
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	Failed      bool      `json:"failed"`
	LatencyMS   int64     `json:"latency_ms"`
	TTFTMS      *int64    `json:"ttft_ms"`
	Tokens      int64     `json:"tokens"`
	Time        time.Time `json:"time"`
}
