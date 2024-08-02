CREATE TABLE users (
    user_id TEXT PRIMARY KEY,
    plan TEXT,
    renewal_ts TIMESTAMP,
    alert_webhook TEXT,
    remaining_cpyt_bal NUMERIC
);
