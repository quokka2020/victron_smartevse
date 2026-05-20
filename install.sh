#!/bin/bash
SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
SERVICE_NAME=victron_smartevse

echo
echo "Installing $SERVICE_NAME..."

chmod 755 $SCRIPT_DIR/$SERVICE_NAME
chmod 755 $SCRIPT_DIR/install.sh
chmod 755 $SCRIPT_DIR/restart.sh
chmod 755 $SCRIPT_DIR/stop.sh

chmod 755 $SCRIPT_DIR/service/run
chmod 755 $SCRIPT_DIR/service/log/run

if [ ! -L /service/$SERVICE_NAME ]; then
    echo "Creating service..."
    ln -s $SCRIPT_DIR/service /service/$SERVICE_NAME
else
    echo "Service already exists."
fi

filename=/data/rc.local
if [ ! -f $filename ]
then
    touch $filename
    chmod 755 $filename
    echo "#!/bin/bash" >> $filename
    echo >> $filename
fi
grep -qxF "bash $SCRIPT_DIR/install.sh" $filename || echo "bash $SCRIPT_DIR/install.sh" >> $filename
