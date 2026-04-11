#!/bin/bash -e
# Tail application logs on a Cerbo GX over SSH.

CERBO_HOST="${CERBO_HOST:-127.0.0.1}"
CERBO_USER="${CERBO_USER:-root}"
CERBO_SSH_PORT="${CERBO_SSH_PORT:-2222}"
REMOTE_LOG_FILE="${REMOTE_LOG_FILE:-/tmp/smartevse.log}"

SSH_OPTS="-p ${CERBO_SSH_PORT} -o StrictHostKeyChecking=no"

echo "Tailing ${REMOTE_LOG_FILE} on ${CERBO_USER}@${CERBO_HOST}:${CERBO_SSH_PORT}"
exec ssh ${SSH_OPTS} "${CERBO_USER}@${CERBO_HOST}" "touch '${REMOTE_LOG_FILE}' && tail -n 200 -F '${REMOTE_LOG_FILE}'"

