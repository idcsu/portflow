package store

import (
	"context"
	"errors"
	"time"

	"portflow/internal/domain"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("already exists")
	ErrBootstrapCompleted = errors.New("initial administrator already exists")
	ErrTokenInvalid       = errors.New("token is invalid, expired, or already used")
	ErrLastAdministrator  = errors.New("at least one active administrator is required")
)

const (
	MaxStoredConnectionsPerNode = 4000
	NodeMetricRetention         = 30 * 24 * time.Hour
	AgentLogRetention           = 14 * 24 * time.Hour
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type User struct {
	ID                string    `json:"id"`
	Username          string    `json:"username"`
	PasswordHash      string    `json:"-"`
	Role              Role      `json:"role"`
	Disabled          bool      `json:"disabled"`
	MFAEnabled        bool      `json:"mfaEnabled"`
	MFASecret         string    `json:"-"`
	MFARecoveryHashes []string  `json:"-"`
	CreatedAt         time.Time `json:"createdAt"`
}

type UserUpdate struct {
	Role         Role
	Disabled     bool
	PasswordHash string
	ResetMFA     bool
}

type Session struct {
	ID        string
	UserID    string
	TokenHash string
	ExpiresAt time.Time
	CreatedAt time.Time
}

type EnrollmentToken struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	TokenHash string     `json:"-"`
	CreatedBy string     `json:"createdBy"`
	ExpiresAt time.Time  `json:"expiresAt"`
	CreatedAt time.Time  `json:"createdAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
}

type AgentIdentity struct {
	ID             string
	Name           string
	Region         string
	PublicIP       string
	Architecture   string
	AgentVersion   string
	CredentialHash string
	CreatedAt      time.Time
}

type AuditEvent struct {
	ID         string                 `json:"id"`
	ActorType  string                 `json:"actorType"`
	ActorID    string                 `json:"actorId,omitempty"`
	Action     string                 `json:"action"`
	TargetType string                 `json:"targetType"`
	TargetID   string                 `json:"targetId,omitempty"`
	RemoteIP   string                 `json:"remoteIp,omitempty"`
	Details    map[string]interface{} `json:"details,omitempty"`
	CreatedAt  time.Time              `json:"createdAt"`
}

type MetricSample struct {
	NodeID        string
	SampledAt     time.Time
	CPUPercent    float64
	MemoryPercent float64
	LoadOne       float64
	DiskPercent   float64
	NetworkRxBps  uint64
	NetworkTxBps  uint64
	ActiveConns   uint64
	BytesIn       uint64
	BytesOut      uint64
}

type AgentLog struct {
	NodeID     string    `json:"nodeId"`
	EventID    string    `json:"id"`
	Level      string    `json:"level"`
	Component  string    `json:"component"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
	ReceivedAt time.Time `json:"receivedAt"`
}

type AgentLogFilter struct {
	NodeID string
	Level  string
	Limit  int
	Before time.Time
}

type TrafficPoint struct {
	Time          time.Time `json:"time"`
	UploadBytes   uint64    `json:"uploadBytes"`
	DownloadBytes uint64    `json:"downloadBytes"`
}

type NodeMetricPoint struct {
	Time          time.Time `json:"time"`
	CPUPercent    float64   `json:"cpuPercent"`
	MemoryPercent float64   `json:"memoryPercent"`
	LoadOne       float64   `json:"loadOne"`
	DiskPercent   float64   `json:"diskPercent"`
	NetworkRxBps  uint64    `json:"networkRxBps"`
	NetworkTxBps  uint64    `json:"networkTxBps"`
	ActiveConns   uint64    `json:"activeConnections"`
	UploadBytes   uint64    `json:"uploadBytes"`
	DownloadBytes uint64    `json:"downloadBytes"`
}

type AgentHeartbeat struct {
	PublicIP               string
	TunnelAddress          string
	AgentVersion           string
	ConfigVersion          uint64
	AttemptedConfigVersion uint64
	LastConfigError        string
	CPUPercent             float64
	MemoryPercent          float64
	LoadOne                float64
	DiskPercent            float64
	NetworkRxBps           uint64
	NetworkTxBps           uint64
	ActiveConns            uint64
	BytesIn                uint64
	BytesOut               uint64
	RuleStats              []RuleRuntimeStats
	Connections            []ActiveConnection
	ConnectionsComplete    bool
	Logs                   []AgentLog
	ReceivedAt             time.Time
}

type ActiveConnection struct {
	NodeID        string    `json:"nodeId"`
	ConnectionID  string    `json:"id"`
	RuleID        string    `json:"ruleId"`
	Protocol      string    `json:"protocol"`
	SourceAddress string    `json:"sourceAddress"`
	TargetAddress string    `json:"targetAddress"`
	StartedAt     time.Time `json:"startedAt"`
	LastActivity  time.Time `json:"lastActivity"`
	BytesIn       uint64    `json:"bytesIn"`
	BytesOut      uint64    `json:"bytesOut"`
	ObservedAt    time.Time `json:"observedAt"`
}

type RuleRuntimeStats struct {
	RuleID      string
	ActiveConns uint64
	BytesIn     uint64
	BytesOut    uint64
}

type AgentConfig struct {
	Version uint64
	Rules   []domain.ForwardRule
}

type Store interface {
	Close() error
	Health(context.Context) error
	CreateInitialUser(context.Context, User) error
	CreateUser(context.Context, User) error
	UpdateUser(context.Context, string, UserUpdate) (User, error)
	DeleteUser(context.Context, string) error
	SetUserMFA(context.Context, string, bool, string, []string) (User, error)
	ConsumeRecoveryCode(context.Context, string, string) (bool, error)
	ListUsers(context.Context) ([]User, error)
	UserByUsername(context.Context, string) (User, error)
	UserByID(context.Context, string) (User, error)
	CreateSession(context.Context, Session) error
	SessionUserByTokenHash(context.Context, string, time.Time) (Session, User, error)
	DeleteSessionByTokenHash(context.Context, string) error
	CreateEnrollmentToken(context.Context, EnrollmentToken) error
	EnrollAgent(context.Context, string, time.Time, AgentIdentity) (domain.Node, error)
	UpdateAgentHeartbeat(context.Context, string, string, AgentHeartbeat) (domain.Node, error)
	ConfigForAgent(context.Context, string, string) (AgentConfig, error)
	ListNodes(context.Context) ([]domain.Node, error)
	CreateForwardRule(context.Context, domain.ForwardRule) (domain.ForwardRule, error)
	UpdateForwardRule(context.Context, domain.ForwardRule) (domain.ForwardRule, error)
	DeleteForwardRule(context.Context, string) error
	ListForwardRules(context.Context) ([]domain.ForwardRule, error)
	RecordAudit(context.Context, AuditEvent) error
	ListAudit(context.Context, int, time.Time) ([]AuditEvent, error)
	ListAgentLogs(context.Context, AgentLogFilter) ([]AgentLog, error)
	ListActiveConnections(context.Context) ([]ActiveConnection, error)
	TrafficHistory(context.Context, time.Time, time.Time, time.Duration) ([]TrafficPoint, error)
	NodeMetricHistory(context.Context, string, time.Time, time.Time, time.Duration) ([]NodeMetricPoint, error)
}
