# Hanzo Cloud AI Service — ZAP Schema
#
# Server: cloud-api (Go/Beego) at cloud.hanzo.ai / api.hanzo.ai
#
# Code generation:
#   zapc generate cloud.zap --lang go --out ./gen/zap/
#   zapc generate cloud.zap --lang ts --out ./gen/zap/
#   zapc generate cloud.zap --lang py --out ./gen/zap/
#   zapc generate cloud.zap --lang rust --out ./gen/zap/

# ── Enums ────────────────────────────────────────────────────────────────

enum ModelProvider
  openai
  anthropic
  google
  together
  groq
  deepseek
  fireworks
  mistral
  cohere
  bedrock
  zen

enum FinishReason
  stop
  length
  content_filter
  tool_calls
  function_call

enum MessageRole
  system
  user
  assistant
  tool
  function

enum StreamEvent
  chunk
  done
  error

# ── Chat Completions ─────────────────────────────────────────────────────

struct ChatMessage
  role          Text
  content       Text
  name          Text
  toolCalls     List(ToolCall)
  toolCallId    Text

struct ToolCall
  id        Text
  type      Text
  function  FunctionCall

struct FunctionCall
  name      Text
  arguments Text

struct ChatRequest
  model         Text
  messages      List(ChatMessage)
  stream        Bool
  temperature   Float32
  maxTokens     Int32
  topP          Float32
  topK          Int32
  stop          List(Text)
  tools         List(ToolDef)
  toolChoice    Text
  user          Text
  seed          Int64
  n             Int32
  presencePen   Float32
  frequencyPen  Float32
  logprobs      Bool
  topLogprobs   Int32

struct ToolDef
  type      Text
  function  FunctionDef

struct FunctionDef
  name        Text
  description Text
  parameters  Text

struct ChatResponse
  id          Text
  object      Text
  created     Int64
  model       Text
  choices     List(Choice)
  usage       TokenUsage
  systemFp    Text

struct Choice
  index         Int32
  message       ChatMessage
  finishReason  Text
  logprobs      Text

struct StreamChunk
  id          Text
  object      Text
  created     Int64
  model       Text
  choices     List(StreamChoice)
  usage       TokenUsage

struct StreamChoice
  index         Int32
  delta         ChatMessage
  finishReason  Text

struct TokenUsage
  promptTokens      Int32
  completionTokens  Int32
  totalTokens       Int32

# ── Models ───────────────────────────────────────────────────────────────

struct ModelInfo
  id          Text
  object      Text
  created     Int64
  ownedBy     Text
  premium     Bool
  contextLen  Int32
  maxOutput   Int32

struct ModelList
  object  Text
  data    List(ModelInfo)

# ── Billing ──────────────────────────────────────────────────────────────

struct Balance
  user      Text
  balance   Float64
  currency  Text
  available Float64

struct UsageRecord
  id          Text
  user        Text
  model       Text
  provider    Text
  promptTok   Int32
  completTok  Int32
  totalTok    Int32
  cost        Float64
  timestamp   Int64

# ── Embeddings ───────────────────────────────────────────────────────────

struct EmbeddingRequest
  model   Text
  input   List(Text)
  user    Text

struct EmbeddingData
  index     Int32
  embedding List(Float32)
  object    Text

struct EmbeddingResponse
  object  Text
  data    List(EmbeddingData)
  model   Text
  usage   EmbeddingUsage

struct EmbeddingUsage
  promptTokens  Int32
  totalTokens   Int32

# ── Knowledge / RAG ─────────────────────────────────────────────────────

struct Store
  name        Text
  displayName Text
  provider    Text

struct VectorSearchRequest
  store       Text
  query       Text
  topK        Int32
  threshold   Float32

struct VectorSearchResult
  id        Text
  score     Float32
  content   Text
  metadata  Text

# ── Service Interface ────────────────────────────────────────────────────

interface CloudService
  # Chat completions (OpenAI-compatible)
  chatCompletions (request ChatRequest) -> (response ChatResponse)

  # Streaming chat completions (returns stream of StreamChunk)
  chatCompletionsStream (request ChatRequest) -> (chunk StreamChunk)

  # List available models
  listModels () -> (list ModelList)

  # Get user balance
  getBalance (user Text, currency Text) -> (balance Balance)

  # Create embeddings
  createEmbeddings (request EmbeddingRequest) -> (response EmbeddingResponse)

  # Vector search (RAG)
  vectorSearch (request VectorSearchRequest) -> (results List(VectorSearchResult))

  # Record usage (internal, from gateway)
  recordUsage (record UsageRecord) -> ()
