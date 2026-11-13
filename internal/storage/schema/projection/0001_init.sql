CREATE TABLE channels (
  channel_id               INTEGER PRIMARY KEY CHECK (channel_id > 0),
  kind                     TEXT    NOT NULL CHECK (kind IN ('registry','bucket')),
  access_hash              INTEGER NOT NULL DEFAULT 0,
  title                    TEXT    NOT NULL DEFAULT '',
  state                    TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','deleted')),
  last_synced_msg          INTEGER NOT NULL DEFAULT 0 CHECK (last_synced_msg >= 0),
  pts                      INTEGER NOT NULL DEFAULT 0 CHECK (pts >= 0),
  initial_sync_done        INTEGER NOT NULL DEFAULT 0 CHECK (initial_sync_done IN (0,1)),
  needs_projection_rebuild INTEGER NOT NULL DEFAULT 0 CHECK (needs_projection_rebuild IN (0,1)),
  last_reconciled_at       INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE UNIQUE INDEX channels_one_registry ON channels (kind) WHERE kind = 'registry';

CREATE TABLE replay_log (
  channel_id      INTEGER NOT NULL,
  msg_id          INTEGER NOT NULL CHECK (msg_id > 0),
  op_type         TEXT    NOT NULL CHECK (length(op_type) > 0),
  op_payload_json TEXT    NOT NULL,
  raw_header      TEXT    NOT NULL,
  first_seen_hash TEXT    NOT NULL CHECK (length(first_seen_hash) = 64),
  object_key      TEXT,
  job_id          TEXT    NOT NULL DEFAULT '',
  is_document     INTEGER NOT NULL CHECK (is_document IN (0,1)),
  actor_user_id   INTEGER NOT NULL DEFAULT 0,
  msg_date        INTEGER NOT NULL CHECK (msg_date >= 0),
  PRIMARY KEY (channel_id, msg_id)
) STRICT;

CREATE INDEX replay_log_key ON replay_log (channel_id, object_key, msg_id) WHERE object_key IS NOT NULL;

CREATE TABLE replay_log_tamper (
  channel_id     INTEGER NOT NULL,
  msg_id         INTEGER NOT NULL,
  old_hash       TEXT    NOT NULL CHECK (length(old_hash) = 64),
  new_hash       TEXT    NOT NULL CHECK (length(new_hash) = 64),
  new_raw_header TEXT    NOT NULL,
  observations   INTEGER NOT NULL DEFAULT 1 CHECK (observations >= 1),
  PRIMARY KEY (channel_id, msg_id, new_hash)
) STRICT;

CREATE TABLE replay_log_rejects (
  channel_id INTEGER NOT NULL,
  msg_id     INTEGER NOT NULL CHECK (msg_id > 0),
  code       TEXT    NOT NULL CHECK (length(code) > 0),
  op_type    TEXT    NOT NULL DEFAULT '',
  job_id     TEXT    NOT NULL DEFAULT '',
  raw_header TEXT    NOT NULL,
  detail     TEXT    NOT NULL DEFAULT '',
  msg_date   INTEGER NOT NULL CHECK (msg_date >= 0),
  PRIMARY KEY (channel_id, msg_id)
) STRICT;

CREATE INDEX replay_log_rejects_code ON replay_log_rejects (code, channel_id, msg_id);

CREATE TABLE registry_markers (
  channel_id      INTEGER NOT NULL,
  msg_id          INTEGER NOT NULL CHECK (msg_id > 0),
  deployment_id   TEXT    NOT NULL CHECK (length(deployment_id) = 26),
  format_version  INTEGER NOT NULL CHECK (format_version >= 1),
  min_reader      INTEGER NOT NULL CHECK (min_reader BETWEEN 1 AND format_version),
  kek_fingerprint TEXT    NOT NULL CHECK (length(kek_fingerprint) = 37 AND substr(kek_fingerprint, 1, 5) = 'bkf1_'),
  primary_user_id INTEGER NOT NULL CHECK (primary_user_id > 0),
  writer          TEXT    NOT NULL DEFAULT '',
  msg_date        INTEGER NOT NULL CHECK (msg_date >= 0),
  PRIMARY KEY (channel_id, msg_id)
) STRICT;

CREATE TABLE buckets (
  channel_id            INTEGER PRIMARY KEY CHECK (channel_id > 0),
  name                  TEXT    NOT NULL CHECK (length(name) BETWEEN 3 AND 63),
  policy                TEXT    NOT NULL CHECK (policy IN ('private','public-read')),
  encryption_default    TEXT    NOT NULL CHECK (encryption_default IN ('none','tde1')),
  cache_max_age_seconds INTEGER NOT NULL CHECK (cache_max_age_seconds BETWEEN 0 AND 31536000),
  created_msg_id        INTEGER NOT NULL CHECK (created_msg_id > 0),
  created_job_id        TEXT    NOT NULL CHECK (length(created_job_id) = 36),
  created_date          INTEGER NOT NULL CHECK (created_date >= 0),
  config_msg_id         INTEGER NOT NULL CHECK (config_msg_id >= created_msg_id),
  deleted_msg_id        INTEGER NOT NULL DEFAULT 0 CHECK (deleted_msg_id >= 0),
  object_count          INTEGER NOT NULL DEFAULT 0 CHECK (object_count >= 0),
  stored_bytes          INTEGER NOT NULL DEFAULT 0 CHECK (stored_bytes >= 0),
  plaintext_bytes       INTEGER NOT NULL DEFAULT 0 CHECK (plaintext_bytes >= 0)
) STRICT;

CREATE UNIQUE INDEX buckets_live_name ON buckets (name) WHERE deleted_msg_id = 0;

CREATE TABLE objects (
  channel_id         INTEGER NOT NULL,
  object_key         TEXT    NOT NULL CHECK (length(CAST(object_key AS BLOB)) BETWEEN 1 AND 1024),
  put_msg_id         INTEGER NOT NULL CHECK (put_msg_id > 0),
  body_msg_id        INTEGER NOT NULL CHECK (body_msg_id >= 0 AND body_msg_id < put_msg_id),
  job_id             TEXT    NOT NULL CHECK (length(job_id) = 36),
  stored_size        INTEGER NOT NULL CHECK (stored_size BETWEEN 0 AND 1992294400),
  plaintext_size     INTEGER NOT NULL CHECK (plaintext_size >= 0),
  etag               TEXT    NOT NULL CHECK (length(etag) BETWEEN 32 AND 38),
  sha256             TEXT    NOT NULL CHECK (length(sha256) = 64),
  content_type       TEXT    NOT NULL CHECK (length(content_type) BETWEEN 1 AND 1024),
  user_meta_json     TEXT    NOT NULL DEFAULT '{}',
  system_meta_json   TEXT    NOT NULL DEFAULT '{}',
  encrypted          INTEGER NOT NULL CHECK (encrypted IN (0,1)),
  encryption_version INTEGER NOT NULL DEFAULT 0 CHECK (encryption_version >= 0),
  key_id             TEXT    NOT NULL DEFAULT '',
  writer             TEXT    NOT NULL,
  op_ts              INTEGER NOT NULL CHECK (op_ts >= 0),
  msg_date           INTEGER NOT NULL CHECK (msg_date >= 0),
  PRIMARY KEY (channel_id, object_key),
  CHECK ((encrypted = 1 AND encryption_version >= 1 AND length(key_id) = 16) OR (encrypted = 0 AND encryption_version = 0 AND key_id = '' AND plaintext_size = stored_size)),
  CHECK (body_msg_id > 0 OR stored_size = 0)
) STRICT;

CREATE UNIQUE INDEX objects_put ON objects (channel_id, put_msg_id);
CREATE INDEX objects_body ON objects (channel_id, body_msg_id) WHERE body_msg_id > 0;

CREATE TABLE object_tombstones (
  channel_id        INTEGER NOT NULL,
  object_key        TEXT    NOT NULL CHECK (length(CAST(object_key AS BLOB)) BETWEEN 1 AND 1024),
  del_msg_id        INTEGER NOT NULL CHECK (del_msg_id > 0),
  job_id            TEXT    NOT NULL CHECK (length(job_id) = 36),
  target_put_msg_id INTEGER NOT NULL DEFAULT 0 CHECK (target_put_msg_id >= 0),
  writer            TEXT    NOT NULL,
  msg_date          INTEGER NOT NULL CHECK (msg_date >= 0),
  PRIMARY KEY (channel_id, object_key)
) STRICT;

CREATE TABLE object_bodies (
  channel_id         INTEGER NOT NULL,
  msg_id             INTEGER NOT NULL CHECK (msg_id > 0),
  job_id             TEXT    NOT NULL CHECK (length(job_id) = 36),
  stored_size        INTEGER NOT NULL CHECK (stored_size BETWEEN 1 AND 1992294400),
  encrypted          INTEGER NOT NULL CHECK (encrypted IN (0,1)),
  encryption_version INTEGER NOT NULL DEFAULT 0 CHECK (encryption_version >= 0),
  msg_date           INTEGER NOT NULL CHECK (msg_date >= 0),
  PRIMARY KEY (channel_id, msg_id)
) STRICT;

CREATE INDEX object_bodies_job ON object_bodies (channel_id, job_id);
CREATE INDEX object_bodies_age ON object_bodies (channel_id, msg_date, msg_id);

CREATE TABLE encryption_keys (
  bucket_channel_id INTEGER NOT NULL,
  key_id            TEXT    NOT NULL CHECK (length(key_id) = 16),
  wrapped_dek       BLOB    NOT NULL CHECK (length(wrapped_dek) = 72),
  key_check         BLOB    NOT NULL CHECK (length(key_check) = 61),
  kek_fingerprint   TEXT    NOT NULL CHECK (length(kek_fingerprint) = 37 AND substr(kek_fingerprint, 1, 5) = 'bkf1_'),
  config_version    INTEGER NOT NULL CHECK (config_version >= 1),
  first_msg_id      INTEGER NOT NULL CHECK (first_msg_id > 0),
  msg_id            INTEGER NOT NULL CHECK (msg_id >= first_msg_id),
  job_id            TEXT    NOT NULL CHECK (length(job_id) = 36),
  msg_date          INTEGER NOT NULL CHECK (msg_date >= 0),
  PRIMARY KEY (bucket_channel_id, key_id)
) STRICT;

CREATE INDEX encryption_keys_active ON encryption_keys (bucket_channel_id, kek_fingerprint, first_msg_id);

CREATE TABLE projection_operations (
  channel_id INTEGER NOT NULL,
  op_id      TEXT    NOT NULL CHECK (length(op_id) = 36),
  op_type    TEXT    NOT NULL,
  msg_id     INTEGER NOT NULL CHECK (msg_id > 0),
  outcome    TEXT    NOT NULL CHECK (outcome IN ('applied','superseded','rejected')),
  error      TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (channel_id, op_id, op_type)
) STRICT;

CREATE INDEX projection_operations_msg ON projection_operations (channel_id, msg_id);

CREATE TABLE initial_scan_spool (
  channel_id    INTEGER NOT NULL,
  msg_id        INTEGER NOT NULL,
  from_id       INTEGER NOT NULL,
  date          INTEGER NOT NULL,
  text          TEXT    NOT NULL,
  has_media     INTEGER NOT NULL CHECK (has_media IN (0,1)),
  media_size    INTEGER NOT NULL,
  document_name TEXT    NOT NULL,
  PRIMARY KEY (channel_id, msg_id)
) STRICT;

CREATE TABLE deleted_messages (
  channel_id  INTEGER NOT NULL,
  msg_id      INTEGER NOT NULL CHECK (msg_id > 0),
  source      TEXT    NOT NULL CHECK (source IN ('external','cleanup','reconcile')),
  recorded_at INTEGER NOT NULL,
  PRIMARY KEY (channel_id, msg_id)
) STRICT;

CREATE TABLE pending_message_cleanup (
  channel_id       INTEGER NOT NULL,
  msg_id           INTEGER NOT NULL CHECK (msg_id > 0),
  reason           TEXT    NOT NULL CHECK (reason IN ('superseded','blocked','duplicate','deleted','orphan_body')),
  object_key       TEXT,
  queued_by_msg_id INTEGER NOT NULL DEFAULT 0 CHECK (queued_by_msg_id >= 0),
  attempts         INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at  INTEGER NOT NULL DEFAULT 0,
  last_error       TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (channel_id, msg_id)
) STRICT;

CREATE INDEX pending_message_cleanup_due ON pending_message_cleanup (next_attempt_at, channel_id, msg_id);

CREATE TABLE pending_channel_cleanup (
  channel_id      INTEGER PRIMARY KEY CHECK (channel_id > 0),
  bdel_msg_id     INTEGER NOT NULL CHECK (bdel_msg_id > 0),
  attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT    NOT NULL DEFAULT ''
) STRICT;
