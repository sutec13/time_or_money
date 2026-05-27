CREATE TABLE IF NOT EXISTS locks (
  id BIGSERIAL PRIMARY KEY,
  secret_text TEXT NOT NULL,
  unlock_at TEXT NOT NULL,
  unlock_local TEXT NOT NULL DEFAULT '',
  timezone_name TEXT NOT NULL DEFAULT 'UTC',
  timezone_offset_minutes INTEGER NOT NULL DEFAULT 0,
  price_amount INTEGER NOT NULL DEFAULT 500,
  currency TEXT NOT NULL DEFAULT 'JPY',
  unlocked INTEGER NOT NULL DEFAULT 0,
  unlock_reason TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS purchase_events (
  id BIGSERIAL PRIMARY KEY,
  lock_id BIGINT NOT NULL,
  amount INTEGER NOT NULL,
  currency TEXT NOT NULL DEFAULT 'JPY',
  provider TEXT NOT NULL,
  provider_payment_id TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  lock_preview TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_purchase_events_lock_id ON purchase_events(lock_id);
