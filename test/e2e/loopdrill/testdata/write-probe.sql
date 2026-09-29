-- 쓰기 탐침 역할·표(설계 §3.12·§3.13) — primary 에서 로컬 superuser 로 돈다. 훈련장과 운영 L1 이 같은 파일을 쓴다.
-- 비밀번호는 stdin 첫 줄 → 환경 PROBE_PASSWORD(\getenv) — 파일·argv 에 싣지 않는다. 다시 돌려도 같다(멱등).
-- 끝나면(운영 L1 뒤) 걷는다: DROP SCHEMA write_probe CASCADE; DROP ROLE write_probe;
\getenv probe_password PROBE_PASSWORD
DO $$
BEGIN
  CREATE ROLE write_probe LOGIN;
EXCEPTION WHEN duplicate_object THEN
  NULL;
END
$$;
ALTER ROLE write_probe LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD :'probe_password';
CREATE SCHEMA IF NOT EXISTS write_probe AUTHORIZATION write_probe;
CREATE TABLE IF NOT EXISTS write_probe.acks (
  id bigserial PRIMARY KEY,
  at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE write_probe.acks OWNER TO write_probe;
