CREATE TABLE IF NOT EXISTS ingestion_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL
        REFERENCES documents(id)
        ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'processing', 'completed', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0
        CHECK (attempts >= 0),
    last_error TEXT,
    claimed_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (document_id)
);

CREATE INDEX IF NOT EXISTS ingestion_jobs_claimable_idx
    ON ingestion_jobs (claimed_until)
    WHERE state IN ('queued', 'processing');
