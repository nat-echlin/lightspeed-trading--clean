CREATE TABLE trades (
    id SERIAL PRIMARY KEY,
    trader_uid TEXT,
    trade_type TEXT,
    symbol TEXT,
    side TEXT,
    qty REAL,
    utc_timestamp TIMESTAMP,
    entry_price NUMERIC,
    market_price REAL,
    pnl REAL
);