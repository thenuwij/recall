ALTER TABLE users
    ALTER COLUMN max_documents SET DEFAULT 10,
    ALTER COLUMN max_pages_per_document SET DEFAULT 50;
