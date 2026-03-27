CREATE TABLE public.plan_limits (
  plan            TEXT PRIMARY KEY,
  db_size_mb      INT NOT NULL,
  storage_mb      INT NOT NULL,
  bandwidth_mb    INT NOT NULL,
  mau_limit       INT NOT NULL,
  rate_limit_rps  INT NOT NULL,
  ws_connections  INT NOT NULL,
  upload_size_mb  INT NOT NULL,
  webhook_limit   INT NOT NULL,     -- 0 = unlimited
  project_limit   INT NOT NULL,
  log_retention_days INT NOT NULL,
  custom_templates BOOLEAN NOT NULL
);

INSERT INTO plan_limits VALUES
  ('free', 500, 1024, 5120, 10000, 100, 100, 10, 3, 2, 1, false),
  ('pro',  5120, 51200, 102400, 100000, 1000, 10000, 50, 0, 10, 30, true);
