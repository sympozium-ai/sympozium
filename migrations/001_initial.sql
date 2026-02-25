-- Sympozium Initial Schema
-- PostgreSQL with pgvector extension for memory embeddings

-- Enable extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "vector";

-- Sessions table: tracks conversation sessions
CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    instance_name VARCHAR(253) NOT NULL,
    namespace VARCHAR(253) NOT NULL DEFAULT 'default',
    session_key VARCHAR(512) NOT NULL,
    channel_type VARCHAR(50),
    sender_id VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB DEFAULT '{}',
    UNIQUE (instance_name, namespace, session_key)
);

CREATE INDEX idx_sessions_instance ON sessions (instance_name, namespace);
CREATE INDEX idx_sessions_key ON sessions (session_key);

-- Transcript events: stores the full conversation history
CREATE TABLE transcript_events (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    sequence_num BIGINT NOT NULL,
    event_type VARCHAR(50) NOT NULL,  -- user_message, agent_message, tool_call, tool_result, error, system
    role VARCHAR(20) NOT NULL,        -- user, assistant, system, tool
    content TEXT,
    tool_name VARCHAR(255),
    tool_input JSONB,
    tool_output JSONB,
    agent_run_name VARCHAR(253),
    agent_id VARCHAR(255),
    tokens_used INTEGER,
    model VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB DEFAULT '{}'
);

CREATE INDEX idx_transcript_session ON transcript_events (session_id, sequence_num);
CREATE INDEX idx_transcript_type ON transcript_events (event_type);
CREATE INDEX idx_transcript_agent_run ON transcript_events (agent_run_name);

-- Memory embeddings: vector store for agent memory/RAG
CREATE TABLE memory_embeddings (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    session_id UUID REFERENCES sessions(id) ON DELETE SET NULL,
    instance_name VARCHAR(253) NOT NULL,
    namespace VARCHAR(253) NOT NULL DEFAULT 'default',
    content TEXT NOT NULL,
    embedding vector(1536),
    source_type VARCHAR(50),          -- transcript, document, code, summary
    source_ref VARCHAR(512),          -- Reference to source (file path, URL, etc.)
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB DEFAULT '{}'
);

CREATE INDEX idx_embeddings_instance ON memory_embeddings (instance_name, namespace);
CREATE INDEX idx_embeddings_session ON memory_embeddings (session_id);

-- Use IVFFlat index for approximate nearest neighbor search
CREATE INDEX idx_embeddings_vector ON memory_embeddings
    USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);

-- Agent run audit log: tracks all agent executions for compliance
CREATE TABLE agent_run_audit (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    agent_run_name VARCHAR(253) NOT NULL,
    instance_name VARCHAR(253) NOT NULL,
    namespace VARCHAR(253) NOT NULL DEFAULT 'default',
    agent_id VARCHAR(255) NOT NULL,
    session_key VARCHAR(512),
    parent_run_name VARCHAR(253),
    task TEXT,
    model VARCHAR(255),
    phase VARCHAR(50) NOT NULL,       -- Pending, Running, Succeeded, Failed
    pod_name VARCHAR(253),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    result TEXT,
    error TEXT,
    exit_code INTEGER,
    tokens_input INTEGER,
    tokens_output INTEGER,
    tool_calls_count INTEGER,
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_instance ON agent_run_audit (instance_name, namespace);
CREATE INDEX idx_audit_run ON agent_run_audit (agent_run_name);
CREATE INDEX idx_audit_phase ON agent_run_audit (phase);
CREATE INDEX idx_audit_time ON agent_run_audit (created_at);

-- Tool execution log: records every tool call for security audit
CREATE TABLE tool_executions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    agent_run_name VARCHAR(253) NOT NULL,
    instance_name VARCHAR(253) NOT NULL,
    namespace VARCHAR(253) NOT NULL DEFAULT 'default',
    tool_name VARCHAR(255) NOT NULL,
    action VARCHAR(50) NOT NULL,      -- allowed, denied, approval_required, approved, rejected
    input JSONB,
    output JSONB,
    duration_ms INTEGER,
    approved_by VARCHAR(255),
    policy_name VARCHAR(253),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tool_exec_run ON tool_executions (agent_run_name);
CREATE INDEX idx_tool_exec_tool ON tool_executions (tool_name);
CREATE INDEX idx_tool_exec_action ON tool_executions (action);
CREATE INDEX idx_tool_exec_time ON tool_executions (created_at);
