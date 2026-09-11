ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS source_type TEXT NOT NULL DEFAULT 'text'
        CHECK (source_type IN ('text', 'pdf'));

ALTER TABLE document_chunks
    ADD COLUMN IF NOT EXISTS page_number INTEGER
        CHECK (page_number >= 1),
    ADD COLUMN IF NOT EXISTS start_offset INTEGER
        CHECK (start_offset >= 0),
    ADD COLUMN IF NOT EXISTS end_offset INTEGER;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'document_chunks_span_check'
    ) THEN
        ALTER TABLE document_chunks
            ADD CONSTRAINT document_chunks_span_check CHECK (end_offset > start_offset);
    END IF;
END $$;
