#!/bin/bash
SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
SERVICE_NAME=victron_smartevse

echo
echo "Restarting $SERVICE_NAME..."

pid=$(pgrep -f "$SCRIPT_DIR/$SERVICE_NAME")
if [ -n "$pid" ]; then
    svc -t /service/$SERVICE_NAME
    pkill -f "$SCRIPT_DIR/$SERVICE_NAME" > /dev/null 2>&1
    echo "done."
else
    echo "driver is not running!"
fi

echo
