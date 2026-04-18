# Make a evcharger in VRM

Use at own risk!

* [victronenergy dbus](https://github.com/victronenergy/venus/wiki/dbus)
* [victronenergy dbus-api](https://github.com/victronenergy/venus/wiki/dbus-api)
* [victronenergy localsettings](https://github.com/victronenergy/localsettings)
* [dingo SmartEVSE-3.5](https://github.com/dingo35/SmartEVSE-3.5)

## Features

* Can detect SmartEVSE by mdns (not always sable)
* Publish MainsMeter/HomeBattery to SmartEVSE

## Problems/unfinished

* Session time not implemented (yet)

## Installation

```shell
# create the binary
./build.sh

#Prep device
ssh <your-victron> mkdir -p /data/smartevse
scp install.sh <your-victron>:/data/smartevse
scp restart.sh <your-victron>:/data/smartevse
scp stop.sh <your-victron>:/data/smartevse

#Adjust smartevse.env and copy (leave logging in /data otherwise /tmp will fill)
scp smartevse.env <your-victron>:/etc/smartevse

#copy 
scp build/victron_smartevse <your-victron>/tmp

#install or replace
ssh <your-victron> /data/smartevse/install.sh
ssh <your-victron> /data/smartevse/replace.sh
```

## Thanks

* Brian Akins who wrote [go-velib](https://github.com/bakins/go-velib)
* mr-manuel who wrote [venus-os_dbus-mqtt-ev-charger](https://github.com/mr-manuel/venus-os_dbus-mqtt-ev-charger)
