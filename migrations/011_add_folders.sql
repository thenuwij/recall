CREATE TABLE IF NOT EXISTS folders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, user_id)
);
ALTER TABLE documents ADD COLUMN IF NOT EXISTS folder_id UUID;
DO $$ BEGIN
    ALTER TABLE documents ADD CONSTRAINT documents_folder_owner_fk
        FOREIGN KEY (folder_id, user_id) REFERENCES folders(id, user_id)
        ON DELETE SET NULL (folder_id);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
CREATE INDEX IF NOT EXISTS folders_user_idx ON folders(user_id);
CREATE INDEX IF NOT EXISTS documents_folder_idx ON documents(folder_id);
