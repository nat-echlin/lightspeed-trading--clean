CREATE TABLE plans (
    name TEXT PRIMARY KEY,
    cost_per_month REAL,
    comment TEXT,
    allowed_ct_instances INT,
    allowed_sign_instances INT,
    max_ct_balance NUMERIC,
    displayable_name TEXT,
    is_sold BOOLEAN,
    initial_payment REAL
);