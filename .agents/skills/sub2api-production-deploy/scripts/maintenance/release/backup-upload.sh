#!/usr/bin/env bash

backup_upload_init() {
  : "${BACKUP_UPLOAD_HOST:?BACKUP_UPLOAD_HOST is required}"
  : "${BACKUP_UPLOAD_USER:?BACKUP_UPLOAD_USER is required}"
  : "${BACKUP_UPLOAD_KEY:?BACKUP_UPLOAD_KEY is required}"
  : "${BACKUP_UPLOAD_PORT:?BACKUP_UPLOAD_PORT is required}"
  [[ $BACKUP_UPLOAD_HOST =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ || $BACKUP_UPLOAD_HOST =~ ^\[?[A-Fa-f0-9:]+\]?$ ]]
  [[ $BACKUP_UPLOAD_USER =~ ^[A-Za-z_][A-Za-z0-9_-]{0,31}$ ]]
  [[ $BACKUP_UPLOAD_KEY == /* && $BACKUP_UPLOAD_KEY != *$'\n'* && $BACKUP_UPLOAD_KEY != *$'\r'* ]]
  [[ $BACKUP_UPLOAD_PORT =~ ^[0-9]{1,5}$ ]]
  (( BACKUP_UPLOAD_PORT >= 1 && BACKUP_UPLOAD_PORT <= 65535 ))
}

backup_upload_send() {
  local artifact=${1:?artifact is required}
  local digest=${2:?digest is required}
  [[ $artifact =~ ^[A-Za-z0-9._-]{1,200}$ ]]
  [[ $digest =~ ^[0-9a-f]{64}$ ]]
  ssh -i "$BACKUP_UPLOAD_KEY" -p "$BACKUP_UPLOAD_PORT" \
    -o BatchMode=yes -o StrictHostKeyChecking=yes -o LogLevel=ERROR \
    "${BACKUP_UPLOAD_USER}@${BACKUP_UPLOAD_HOST}" "upload daily $artifact $digest" 2>/dev/null
}
