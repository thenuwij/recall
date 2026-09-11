CREATE TABLE IF NOT EXISTS cards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chunk_id UUID NOT NULL
        REFERENCES document_chunks(id)
        ON DELETE CASCADE,
    question TEXT NOT NULL
        CHECK (length(btrim(question)) > 0),
    expected_answer TEXT NOT NULL
        CHECK (length(btrim(expected_answer)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS cards_chunk_id_idx
    ON cards (chunk_id);

CREATE TABLE IF NOT EXISTS card_schedule (
    card_id UUID PRIMARY KEY
        REFERENCES cards(id)
        ON DELETE CASCADE,
    due_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    interval_days INTEGER NOT NULL DEFAULT 0
        CHECK (interval_days >= 0),
    ease_factor DOUBLE PRECISION NOT NULL DEFAULT 2.5
        CHECK (ease_factor >= 1.3),
    repetitions INTEGER NOT NULL DEFAULT 0
        CHECK (repetitions >= 0),
    lapses INTEGER NOT NULL DEFAULT 0
        CHECK (lapses >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS card_schedule_due_idx
    ON card_schedule (due_at);

CREATE TABLE IF NOT EXISTS reviews (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    card_id UUID NOT NULL
        REFERENCES cards(id)
        ON DELETE CASCADE,
    user_answer TEXT NOT NULL,
    grade SMALLINT NOT NULL
        CHECK (grade BETWEEN 0 AND 5),
    rationale TEXT,
    reviewed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS reviews_card_id_idx
    ON reviews (card_id, reviewed_at);
