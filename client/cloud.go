// Package client — canonical client interface for Hanzo Cloud.
//
//	import cloud "github.com/hanzoai/cloud/client"
//	var c cloud.Cloud = cloud.NewClient(cfg)

package client

import (
	"context"
	"io"
	"time"
)

// Cloud is the AI-chat + agent-workspace surface: completions,
// streaming completions, embeddings, model registry, app + chat
// persistence, multi-tenant usage accounting.
type Cloud interface {
	// Kind reports the backend identifier (hanzo-cloud).
	Kind() string

	// Complete runs a non-streaming chat completion against the named
	// model. Mirrors the gateway shape but is routed through cloud's
	// app-aware enrichment (per-app prompt prelude, tool registration,
	// memory store).
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResult, error)

	// Stream runs a streaming completion. SSE JSON chunks.
	Stream(ctx context.Context, req CompletionRequest) (io.ReadCloser, error)

	// Embed produces embedding vectors.
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResult, error)

	// ListModels returns the model registry visible to the caller's
	// org. Org-level enablement gates the visible set.
	ListModels(ctx context.Context, orgID string) ([]Model, error)

	// SaveChat persists a conversation. Returns the canonical chat id.
	SaveChat(ctx context.Context, chat Chat) (string, error)

	// GetChat returns a saved chat by id.
	GetChat(ctx context.Context, orgID, chatID string) (*Chat, error)

	// ListChats returns the user's chat history. Newest first.
	ListChats(ctx context.Context, orgID, userID string, opts ListOpts) ([]ChatSummary, error)

	// DeleteChat removes a saved chat. Idempotent.
	DeleteChat(ctx context.Context, orgID, chatID string) error

	// UpsertApp registers a chat app (system prompt, tool registry,
	// memory binding, model defaults) keyed by name+org.
	UpsertApp(ctx context.Context, app App) error

	// ListApps returns the apps visible to the caller's org.
	ListApps(ctx context.Context, orgID string) ([]App, error)

	// GetUsage returns the org's usage summary for a time window.
	GetUsage(ctx context.Context, orgID string, since, until time.Time) (*Usage, error)
}

// CompletionRequest is the chat call shape.
type CompletionRequest struct {
	OrgID, UserID string
	// AppName selects the chat-app (system prompt + tool registry +
	// model default); empty selects the org's default app.
	AppName  string
	Model    string  // optional override of the app's default model
	Messages []Message
	MaxTokens   int
	Temperature float64
	JSONMode    bool
	// MemoryKey ties this turn to a persistent memory store. Empty
	// disables memory for this call.
	MemoryKey string
	// Extra carries backend-specific knobs (system_prompt_cache, seed,
	// top_k).
	Extra map[string]any
}

// Message is one turn. Role: system | user | assistant | tool.
type Message struct {
	Role       string
	Content    string
	Name       string
	ToolCallID string
	ToolCalls  []ToolCall
}

// ToolCall is the model's tool-invocation request.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON string
}

// CompletionResult is the non-streaming response.
type CompletionResult struct {
	ChatID       string // populated when the call persisted to a chat
	Message      Message
	FinishReason string
	UsageDelta   Usage
	ModelUsed    string
}

// EmbedRequest is the embeddings call shape.
type EmbedRequest struct {
	OrgID, UserID string
	Model         string
	Inputs        []string
}

// EmbedResult is the embeddings response.
type EmbedResult struct {
	Vectors    [][]float32
	Model      string
	UsageDelta Usage
}

// Model is the registry entry.
type Model struct {
	ID                    string
	Provider              string
	ContextSize           int
	Capabilities          []string // chat | embed | vision | tool_use | json | streaming
	PriceInUSDPerMillion  float64
	PriceOutUSDPerMillion float64
	// Enabled — true when the model is enabled for the caller's org.
	Enabled bool
}

// Chat is a saved conversation.
type Chat struct {
	ID         string
	OrgID      string
	UserID     string
	AppName    string
	Title      string
	Messages   []Message
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Pinned     bool
	Archived   bool
}

// ChatSummary is the lightweight list entry.
type ChatSummary struct {
	ID         string
	Title      string
	AppName    string
	UpdatedAt  time.Time
	Pinned     bool
	MessageCount int
}

// ListOpts configures the chat list.
type ListOpts struct {
	Page          int
	PerPage       int
	IncludeArchived bool
	IncludePinnedFirst bool
}

// App is a chat-app definition.
type App struct {
	OrgID         string
	Name          string
	DisplayName   string
	SystemPrompt  string
	DefaultModel  string
	// Tools advertises functions the model may call inside this app.
	Tools []Tool
	// MemoryEnabled — true to bind a memory store at MemoryKey scope.
	MemoryEnabled bool
	UpdatedAt     time.Time
}

// Tool is the function-calling shape.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// Usage is the per-org accounting summary.
type Usage struct {
	OrgID            string
	From, To         time.Time
	PromptTokens     int64
	CompletionTokens int64
	EmbeddingTokens  int64
	PriceUSD         float64
	// PerModel — breakdown by model id.
	PerModel map[string]Usage
}
