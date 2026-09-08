ALTER TABLE document_chunks
    ADD COLUMN IF NOT EXISTS embedding vector(1536),
    ADD COLUMN IF NOT EXISTS embedding_model TEXT,
    ADD COLUMN IF NOT EXISTS embedded_at TIMESTAMPTZ;
