package domain

import "time"

// Analysis is the strict structured result returned by an LLM.
type Analysis struct {
	MessageType   string  `json:"message_type"`
	Reasoning     string  `json:"analysis"`
	IsTask        bool    `json:"is_task"`
	Confidence    float64 `json:"confidence"`
	UpdateTaskID  int64   `json:"update_task_id"`
	Title         string  `json:"title"`
	Description   string  `json:"description"`
	Priority      string  `json:"priority"`
	Category      string  `json:"category"`
	Deadline      string  `json:"deadline"`
	ReplyStrategy string  `json:"reply_strategy"`
	DraftReply    string  `json:"draft_reply"`
}

// AnalysisStatus of a stored LLM call.
type AnalysisStatus string

const (
	AnalysisOK    AnalysisStatus = "ok"
	AnalysisError AnalysisStatus = "error"
)

// AnalysisRecord is an audit log entry of one triage call (prompt quality history).
type AnalysisRecord struct {
	ID            int64
	ConnectionID  string
	ChatID        int64
	MessageRowIDs []int64
	InputText     string
	Provider      string
	Model         string
	RawResponse   string
	IsTask        bool
	Confidence    float64
	MessageType   string
	Status        AnalysisStatus
	Error         string
	TaskID        int64
	LatencyMs     int64
	CreatedAt     time.Time
}

// AnalysisStats aggregates triage quality metrics.
type AnalysisStats struct {
	Total        int
	Errors       int
	Noise        int
	WithTask     int
	AvgLatencyMs float64
	ByProvider   map[string]int
}
