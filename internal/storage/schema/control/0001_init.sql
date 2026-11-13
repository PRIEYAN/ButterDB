CREATE TABLE admin_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

CREATE TABLE admin_setup_token (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  token_hash BLOB    NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT;

CREATE TABLE admin_devices (
  device_id        TEXT    PRIMARY KEY,
  name             TEXT    NOT NULL,
  platform         TEXT    NOT NULL,
  client_version   TEXT    NOT NULL,
  created_via      TEXT    NOT NULL CHECK (created_via IN ('setup','pairing_saved_messages','pairing_host')),
  token_hash       BLOB    NOT NULL,
  token_issued_at  INTEGER NOT NULL,
  prev_token_hash  BLOB,
  prev_valid_until INTEGER,
  created_at       INTEGER NOT NULL,
  last_seen_at     INTEGER NOT NULL,
  last_seen_ip     TEXT    NOT NULL,
  revoked_at       INTEGER,
  revoked_by       TEXT
) STRICT;

CREATE TABLE admin_pairing_codes (
  delivery    TEXT    PRIMARY KEY CHECK (delivery IN ('saved_messages','host')),
  code_hash   BLOB    NOT NULL,
  device_name TEXT    NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE admin_audit (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  at              INTEGER NOT NULL,
  actor_type      TEXT    NOT NULL,
  actor_device_id TEXT    NOT NULL DEFAULT '',
  action          TEXT    NOT NULL,
  target          TEXT    NOT NULL DEFAULT '',
  outcome         TEXT    NOT NULL CHECK (outcome IN ('success','failure')),
  error_code      TEXT    NOT NULL DEFAULT '',
  remote_addr     TEXT    NOT NULL DEFAULT '',
  request_id      TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX admin_audit_at ON admin_audit (at DESC);
CREATE INDEX admin_audit_action ON admin_audit (action, at DESC);

CREATE TABLE telegram_accounts (
  role         TEXT    PRIMARY KEY CHECK (role IN ('primary','replica')),
  user_id      INTEGER NOT NULL CHECK (user_id > 0),
  username     TEXT    NOT NULL DEFAULT '',
  phone_masked TEXT    NOT NULL DEFAULT '',
  dc_id        INTEGER NOT NULL DEFAULT 0 CHECK (dc_id >= 0),
  linked       INTEGER NOT NULL CHECK (linked IN (0,1)),
  bound_at     INTEGER NOT NULL,
  linked_at    INTEGER NOT NULL DEFAULT 0,
  unlinked_at  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE UNIQUE INDEX telegram_accounts_user ON telegram_accounts (user_id);

CREATE TABLE access_keys (
  access_key_id TEXT    PRIMARY KEY CHECK (length(access_key_id) = 20 AND substr(access_key_id, 1, 4) = 'BDBK'),
  name          TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
  secret_sealed BLOB    NOT NULL CHECK (length(secret_sealed) = 80),
  scope_buckets TEXT    NOT NULL CHECK (json_valid(scope_buckets) AND json_type(scope_buckets) = 'array'),
  scope_actions TEXT    NOT NULL CHECK (json_valid(scope_actions) AND json_type(scope_actions) = 'array'),
  created_at    INTEGER NOT NULL,
  created_by    TEXT    NOT NULL DEFAULT '',
  updated_at    INTEGER NOT NULL,
  last_used_at  INTEGER NOT NULL DEFAULT 0,
  revoked_at    INTEGER
) STRICT;

CREATE INDEX access_keys_created ON access_keys (created_at DESC, access_key_id);

CREATE TABLE upload_jobs (
  job_id              TEXT    PRIMARY KEY CHECK (length(job_id) = 36),
  kind                TEXT    NOT NULL CHECK (kind IN ('put','copy','multipart')),
  origin              TEXT    NOT NULL CHECK (origin IN ('s3','admin')),
  scope               TEXT    NOT NULL CHECK (scope LIKE 'key:%' OR scope LIKE 'device:%'),
  writer              TEXT    NOT NULL,
  bucket_channel_id   INTEGER NOT NULL CHECK (bucket_channel_id > 0),
  bucket_name         TEXT    NOT NULL,
  object_key          TEXT    NOT NULL CHECK (length(CAST(object_key AS BLOB)) BETWEEN 1 AND 1024),
  multipart_upload_id TEXT,
  state               TEXT    NOT NULL CHECK (state IN ('prepared','body_sending','uncertain_body','body_sent','put_attempted','uncertain_put','committed','aborted')),
  base_put_msg_id     INTEGER NOT NULL DEFAULT 0 CHECK (base_put_msg_id >= 0),
  if_match            TEXT    NOT NULL DEFAULT '',
  if_none_match       INTEGER NOT NULL DEFAULT 0 CHECK (if_none_match IN (0,1)),
  spool_path          TEXT    NOT NULL,
  sealed_path         TEXT    NOT NULL DEFAULT '',
  plaintext_size      INTEGER NOT NULL CHECK (plaintext_size >= 0),
  stored_size         INTEGER NOT NULL CHECK (stored_size BETWEEN 0 AND 1992294400),
  etag                TEXT    NOT NULL CHECK (length(etag) BETWEEN 32 AND 38),
  sha256              TEXT    NOT NULL CHECK (length(sha256) = 64),
  content_type        TEXT    NOT NULL CHECK (length(content_type) BETWEEN 1 AND 1024),
  user_meta_json      TEXT    NOT NULL DEFAULT '{}',
  system_meta_json    TEXT    NOT NULL DEFAULT '{}',
  encrypted           INTEGER NOT NULL CHECK (encrypted IN (0,1)),
  encryption_version  INTEGER NOT NULL DEFAULT 0 CHECK (encryption_version >= 0),
  key_id              TEXT    NOT NULL DEFAULT '',
  file_salt           BLOB,
  nonce_prefix        BLOB,
  body_msg_id         INTEGER NOT NULL DEFAULT 0 CHECK (body_msg_id >= 0),
  put_header          TEXT    NOT NULL DEFAULT '',
  put_msg_id          INTEGER NOT NULL DEFAULT 0 CHECK (put_msg_id >= 0),
  error_code          TEXT    NOT NULL DEFAULT '',
  attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL,
  CHECK ((encrypted = 1 AND encryption_version >= 1 AND length(key_id) = 16 AND length(file_salt) = 16 AND length(nonce_prefix) = 20)
      OR (encrypted = 0 AND encryption_version = 0 AND key_id = '' AND file_salt IS NULL AND nonce_prefix IS NULL AND plaintext_size = stored_size)),
  CHECK (state NOT IN ('put_attempted','uncertain_put','committed') OR put_header <> ''),
  CHECK (state <> 'committed' OR put_msg_id > 0),
  CHECK (state <> 'body_sent' OR body_msg_id > 0),
  CHECK ((kind = 'multipart') = (multipart_upload_id IS NOT NULL))
) STRICT;

CREATE INDEX upload_jobs_open ON upload_jobs (state, updated_at) WHERE state NOT IN ('committed','aborted');
CREATE INDEX upload_jobs_bucket_open ON upload_jobs (bucket_channel_id) WHERE state NOT IN ('committed','aborted');
CREATE INDEX upload_jobs_terminal ON upload_jobs (updated_at) WHERE state IN ('committed','aborted');

CREATE TABLE delete_jobs (
  job_id            TEXT    PRIMARY KEY CHECK (length(job_id) = 36),
  origin            TEXT    NOT NULL CHECK (origin IN ('s3','admin')),
  scope             TEXT    NOT NULL CHECK (scope LIKE 'key:%' OR scope LIKE 'device:%'),
  writer            TEXT    NOT NULL,
  request_id        TEXT    NOT NULL DEFAULT '',
  bucket_channel_id INTEGER NOT NULL CHECK (bucket_channel_id > 0),
  bucket_name       TEXT    NOT NULL,
  object_key        TEXT    NOT NULL CHECK (length(CAST(object_key AS BLOB)) BETWEEN 1 AND 1024),
  state             TEXT    NOT NULL CHECK (state IN ('prepared','del_attempted','uncertain_del','committed','aborted')),
  target_put_msg_id INTEGER NOT NULL CHECK (target_put_msg_id > 0),
  del_header        TEXT    NOT NULL DEFAULT '',
  del_msg_id        INTEGER NOT NULL DEFAULT 0 CHECK (del_msg_id >= 0),
  error_code        TEXT    NOT NULL DEFAULT '',
  attempts          INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  CHECK (state NOT IN ('del_attempted','uncertain_del','committed') OR del_header <> ''),
  CHECK (state <> 'committed' OR del_msg_id > 0)
) STRICT;

CREATE INDEX delete_jobs_open ON delete_jobs (state, updated_at) WHERE state NOT IN ('committed','aborted');
CREATE INDEX delete_jobs_bucket_open ON delete_jobs (bucket_channel_id) WHERE state NOT IN ('committed','aborted');
CREATE INDEX delete_jobs_terminal ON delete_jobs (updated_at) WHERE state IN ('committed','aborted');

CREATE TABLE idempotency_keys (
  scope             TEXT    NOT NULL CHECK (scope LIKE 'key:%' OR scope LIKE 'device:%'),
  idempotency_key   TEXT    NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
  bucket_channel_id INTEGER NOT NULL CHECK (bucket_channel_id > 0),
  object_key        TEXT    NOT NULL,
  body_sha256       TEXT    NOT NULL CHECK (length(body_sha256) = 64),
  job_id            TEXT    NOT NULL REFERENCES upload_jobs (job_id) ON DELETE CASCADE,
  created_at        INTEGER NOT NULL,
  expires_at        INTEGER NOT NULL CHECK (expires_at > created_at),
  PRIMARY KEY (scope, idempotency_key)
) STRICT;

CREATE INDEX idempotency_keys_expiry ON idempotency_keys (expires_at);
CREATE INDEX idempotency_keys_job ON idempotency_keys (job_id);

CREATE TABLE multipart_uploads (
  upload_id               TEXT    PRIMARY KEY CHECK (length(upload_id) = 32),
  bucket_channel_id       INTEGER NOT NULL CHECK (bucket_channel_id > 0),
  bucket_name             TEXT    NOT NULL,
  object_key              TEXT    NOT NULL CHECK (length(CAST(object_key AS BLOB)) BETWEEN 1 AND 1024),
  initiator_access_key_id TEXT    NOT NULL,
  initiated_at            INTEGER NOT NULL,
  last_activity_at        INTEGER NOT NULL,
  content_type            TEXT    NOT NULL CHECK (length(content_type) BETWEEN 1 AND 1024),
  user_meta_json          TEXT    NOT NULL DEFAULT '{}',
  system_meta_json        TEXT    NOT NULL DEFAULT '{}',
  encrypted               INTEGER NOT NULL CHECK (encrypted IN (0,1)),
  checksum_algorithm      TEXT    NOT NULL DEFAULT '' CHECK (checksum_algorithm IN ('','CRC32','CRC32C','CRC64NVME','SHA1','SHA256')),
  checksum_type           TEXT    NOT NULL DEFAULT '' CHECK (checksum_type IN ('','COMPOSITE','FULL_OBJECT')),
  state                   TEXT    NOT NULL CHECK (state IN ('open','committing','completed','aborted')),
  job_id                  TEXT    NOT NULL DEFAULT '',
  etag                    TEXT    NOT NULL DEFAULT '',
  put_msg_id              INTEGER NOT NULL DEFAULT 0 CHECK (put_msg_id >= 0),
  part_list_sha256        TEXT    NOT NULL DEFAULT '',
  completed_at            INTEGER NOT NULL DEFAULT 0,
  CHECK (state <> 'committing' OR length(job_id) = 36),
  CHECK (state <> 'completed' OR (put_msg_id > 0 AND length(part_list_sha256) = 64 AND completed_at > 0))
) STRICT;

CREATE INDEX multipart_uploads_listing ON multipart_uploads (bucket_channel_id, object_key, initiated_at, upload_id) WHERE state IN ('open','committing');
CREATE INDEX multipart_uploads_idle ON multipart_uploads (last_activity_at) WHERE state = 'open';
CREATE INDEX multipart_uploads_completed ON multipart_uploads (completed_at) WHERE state = 'completed';

CREATE TABLE multipart_parts (
  upload_id      TEXT    NOT NULL REFERENCES multipart_uploads (upload_id) ON DELETE CASCADE,
  part_number    INTEGER NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
  size           INTEGER NOT NULL CHECK (size BETWEEN 0 AND 1992294400),
  md5            TEXT    NOT NULL CHECK (length(md5) = 32),
  checksum_value TEXT    NOT NULL DEFAULT '',
  staged_at      INTEGER NOT NULL,
  PRIMARY KEY (upload_id, part_number)
) STRICT;

CREATE TABLE replica_channels (
  primary_channel_id  INTEGER PRIMARY KEY CHECK (primary_channel_id > 0),
  kind                TEXT    NOT NULL CHECK (kind IN ('registry','bucket')),
  replica_channel_id  INTEGER NOT NULL UNIQUE CHECK (replica_channel_id > 0),
  replica_access_hash INTEGER NOT NULL,
  state               TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','deleted')),
  created_at          INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX replica_channels_one_registry ON replica_channels (kind) WHERE kind = 'registry';

CREATE TABLE replica_jobs (
  seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
  kind                TEXT    NOT NULL CHECK (kind IN ('put','del','purge','bcreate','bcfg','bdel','encfg')),
  primary_channel_id  INTEGER NOT NULL CHECK (primary_channel_id > 0),
  primary_msg_id      INTEGER NOT NULL CHECK (primary_msg_id > 0),
  object_key          TEXT,
  replica_job_id      TEXT    NOT NULL CHECK (length(replica_job_id) = 36),
  state               TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','body_sending','body_sent','op_attempted','done','skipped')),
  replica_body_msg_id INTEGER NOT NULL DEFAULT 0 CHECK (replica_body_msg_id >= 0),
  replica_op_msg_id   INTEGER NOT NULL DEFAULT 0 CHECK (replica_op_msg_id >= 0),
  attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at     INTEGER NOT NULL DEFAULT 0,
  last_error          TEXT    NOT NULL DEFAULT '',
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL,
  UNIQUE (primary_channel_id, primary_msg_id, kind)
) STRICT;

CREATE INDEX replica_jobs_pending ON replica_jobs (state, seq) WHERE state NOT IN ('done','skipped');

CREATE TABLE replica_objects (
  primary_channel_id  INTEGER NOT NULL,
  object_key          TEXT    NOT NULL,
  primary_put_msg_id  INTEGER NOT NULL CHECK (primary_put_msg_id > 0),
  replica_channel_id  INTEGER NOT NULL CHECK (replica_channel_id > 0),
  replica_body_msg_id INTEGER NOT NULL CHECK (replica_body_msg_id >= 0),
  replica_put_msg_id  INTEGER NOT NULL CHECK (replica_put_msg_id > 0),
  mirrored_at         INTEGER NOT NULL,
  PRIMARY KEY (primary_channel_id, object_key)
) STRICT;

CREATE TABLE snapshots (
  snapshot_id            TEXT    PRIMARY KEY CHECK (length(snapshot_id) = 32),
  file_name              TEXT    NOT NULL UNIQUE,
  trigger                TEXT    NOT NULL CHECK (trigger IN ('schedule','command','offline','pre_migrate')),
  created_at             INTEGER NOT NULL,
  bytes                  INTEGER NOT NULL CHECK (bytes > 0),
  sha256                 TEXT    NOT NULL CHECK (length(sha256) = 64),
  server_key_fingerprint TEXT    NOT NULL CHECK (length(server_key_fingerprint) = 37),
  schema_projection      INTEGER NOT NULL CHECK (schema_projection >= 1),
  schema_control         INTEGER NOT NULL CHECK (schema_control >= 1),
  local_state            TEXT    NOT NULL DEFAULT 'present' CHECK (local_state IN ('present','pruned')),
  upload_state           TEXT    NOT NULL DEFAULT 'none' CHECK (upload_state IN ('none','pending','uploaded','failed','too_large','deleted')),
  ops_channel_id         INTEGER NOT NULL DEFAULT 0,
  ops_msg_id             INTEGER NOT NULL DEFAULT 0,
  last_error             TEXT    NOT NULL DEFAULT '',
  CHECK (upload_state NOT IN ('uploaded','deleted') OR (ops_channel_id > 0 AND ops_msg_id > 0))
) STRICT;

CREATE INDEX snapshots_created ON snapshots (created_at DESC);
