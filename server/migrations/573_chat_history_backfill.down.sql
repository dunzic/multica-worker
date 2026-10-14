-- Data-preserving rollback: disable the feature routes/UI instead of deleting conversations.
DO $$ BEGIN RAISE EXCEPTION 'Chat collaboration data is retained. Disable feature routes/UI for application rollback; export and review data before schema removal.'; END $$;
