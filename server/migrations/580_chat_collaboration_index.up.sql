CREATE INDEX CONCURRENTLY idx_atq_topic
    ON agent_task_queue(topic_id)
    WHERE topic_id IS NOT NULL;
