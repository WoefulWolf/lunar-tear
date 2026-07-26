-- +goose Up
CREATE TABLE IF NOT EXISTS user_lifetime_counters (
    user_id        INTEGER NOT NULL PRIMARY KEY REFERENCES users(user_id),
    summon_count   INTEGER NOT NULL DEFAULT 0,
    purchase_count INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO user_lifetime_counters (user_id) SELECT user_id FROM users;

CREATE TABLE IF NOT EXISTS user_daily_mission (
    user_id           INTEGER NOT NULL PRIMARY KEY REFERENCES users(user_id),
    last_reset_date   INTEGER NOT NULL DEFAULT 0,
    clear_baseline    INTEGER NOT NULL DEFAULT 0,
    summon_baseline   INTEGER NOT NULL DEFAULT 0,
    purchase_baseline INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO user_daily_mission (user_id) SELECT user_id FROM users;

-- +goose Down
DROP TABLE IF EXISTS user_daily_mission;
DROP TABLE IF EXISTS user_lifetime_counters;
