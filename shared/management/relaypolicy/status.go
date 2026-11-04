package relaypolicy

import "time"

const (
	// ReportInterval is the normal interval for refreshing applied-path observations.
	ReportInterval = 15 * time.Second
	// ReportExpiry bounds how long observations can be treated as current.
	ReportExpiry = 45 * time.Second
	// MaxReports bounds one authenticated report request.
	MaxReports = 128
	// MaxObservationsPerPeer bounds latest-state rows owned by one endpoint.
	MaxObservationsPerPeer = 1024
)

// PathTransport identifies the actual pair transport independently of home registration.
type PathTransport string

const (
	TransportUnknown     PathTransport = "unknown"
	TransportDirect      PathTransport = "direct"
	TransportNativeRelay PathTransport = "native_relay"
)

// PathState describes whether this endpoint has reconciled its current instructions.
type PathState string

const (
	StatePending     PathState = "pending"
	StateApplied     PathState = "applied"
	StateBlocked     PathState = "blocked"
	StateUnsupported PathState = "unsupported"
)

// FailureReason is a bounded diagnostic code, never an unrestricted remote error string.
type FailureReason string

const (
	ReasonNone                      FailureReason = ""
	ReasonConnecting                FailureReason = "connecting"
	ReasonLazy                      FailureReason = "lazy"
	ReasonNoPermittedRelay          FailureReason = "no_permitted_relay"
	ReasonInvalidPolicy             FailureReason = "invalid_policy"
	ReasonHigherPriorityUnavailable FailureReason = "higher_priority_unavailable"
	ReasonPolicyMismatch            FailureReason = "policy_mismatch"
	ReasonUnsupported               FailureReason = "unsupported"
	ReasonExhausted                 FailureReason = "exhausted"
	ReasonDisconnected              FailureReason = "disconnected"
	ReasonPairTimeout               FailureReason = "pair_timeout"
	ReasonRelayUnavailable          FailureReason = "relay_unavailable"
	ReasonAuthenticationFailed      FailureReason = "authentication_failed"
	ReasonInstanceMismatch          FailureReason = "instance_mismatch"
	ReasonCancelled                 FailureReason = "cancelled"
)

// PathReport is a session-fenced endpoint assertion about one home or pair policy.
// An empty RemotePeerID reports home registration and never describes a pair path.
type PathReport struct {
	RemotePeerID          PeerID
	Revision              uint64
	Digest                Digest
	CatalogGeneration     uint64
	ReselectionGeneration uint64
	Sequence              uint64
	Attempt               uint64
	Transport             PathTransport
	RelayID               RelayID
	State                 PathState
	Reason                FailureReason
}

// EndpointObservation is the latest bounded report received from one authenticated endpoint.
type EndpointObservation struct {
	LocalPeerID PeerID
	Report      PathReport
	ObservedAt  time.Time
	ExpiresAt   time.Time
	Current     bool
}

// ObservationRecord is the separate SQL latest-state model, with sync ownership fences.
// Transport telemetry never changes the peer liveness row.
type ObservationRecord struct {
	AccountID    AccountID `gorm:"primaryKey"`
	LocalPeerID  PeerID    `gorm:"primaryKey"`
	RemotePeerID PeerID    `gorm:"primaryKey"`
	Session      int64
	InstanceID   string
	Sequence     uint64
	Report       PathReport `gorm:"serializer:json"`
	ObservedAt   time.Time
	ExpiresAt    time.Time `gorm:"index"`
}

// TableName returns the separate latest-observation table.
func (ObservationRecord) TableName() string { return "relay_observations" }

// ApplicationState distinguishes converged configuration from transport health.
type ApplicationState string

const (
	ApplicationUnknown      ApplicationState = "unknown"
	ApplicationPending      ApplicationState = "pending"
	ApplicationApplied      ApplicationState = "applied"
	ApplicationUnsupported  ApplicationState = "unsupported"
	ApplicationStale        ApplicationState = "stale"
	ApplicationDisagreement ApplicationState = "disagreement"
	ApplicationUnconfigured ApplicationState = "unconfigured"
)

// EffectiveStatus separates desired policy, endpoint application and observed health.
type EffectiveStatus struct {
	PeerA       PeerID
	PeerB       PeerID
	Policy      *ResolvedRelayPolicy
	Application ApplicationState
	Healthy     bool
	Endpoints   []EndpointObservation
	Transport   PathTransport
	RelayID     RelayID
}

// ValidReason reports whether a reason belongs to the bounded public diagnostic vocabulary.
func ValidReason(reason FailureReason) bool {
	switch reason {
	case ReasonNone, ReasonConnecting, ReasonLazy, ReasonNoPermittedRelay, ReasonInvalidPolicy,
		ReasonHigherPriorityUnavailable, ReasonPolicyMismatch, ReasonUnsupported, ReasonExhausted,
		ReasonDisconnected, ReasonPairTimeout, ReasonRelayUnavailable, ReasonAuthenticationFailed,
		ReasonInstanceMismatch, ReasonCancelled:
		return true
	default:
		return false
	}
}
