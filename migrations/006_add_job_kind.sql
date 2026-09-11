ALTER TABLE ingestion_jobs
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'embed'
        CHECK (kind IN ('embed', 'generate_cards'));

ALTER TABLE ingestion_jobs
    DROP CONSTRAINT IF EXISTS ingestion_jobs_document_id_key;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'ingestion_jobs_document_id_kind_key'
    ) THEN
        ALTER TABLE ingestion_jobs
            ADD CONSTRAINT ingestion_jobs_document_id_kind_key UNIQUE (document_id, kind);
    END IF;
END $$;
