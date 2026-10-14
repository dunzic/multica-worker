CREATE INDEX CONCURRENTLY idx_attachment_storage_url ON attachment(lower(regexp_replace(url, '^https?://[^/]+', '')));
