# victron_smartevse

Integration driver to connect a SmartEVSE charger to a Victron GX device (e.g. Cerbo GX).

This driver creates a Victron EV Charger service and bridges data between SmartEVSE (via MQTT) and Victron (via D-Bus).

---

## ⚠️ Disclaimer

- Not an official Victron or SmartEVSE integration
- Tested with SmartEVSE chargers on one GX system
- Contains hardcoded assumptions (see section below)

---

## Features

- SmartEVSE appears as EV charger in Victron UI / VRM
- Bi-directional data flow:
    - SmartEVSE → Victron (status, power, energy)
    - Victron → SmartEVSE (grid current, battery current, mode)
- mDNS discovery or static IP configuration

---

## Architecture

### SmartEVSE → Victron

Data received via MQTT and mapped to Victron D-Bus:

- Charging state
- Mode
- Per-phase current
- Power and energy
- Temperature

### Victron → SmartEVSE

Published every 2 seconds:

- `Set/MainsMeter` → grid current
- `Set/HomeBatteryCurrent` → battery current
- `Set/Mode` → charge mode

---

## Prerequisites

- Victron GX device (Cerbo GX recommended)
- Venus OS installed
- SmartEVSE with:
    - Network access
    - MQTT enabled
- MQTT broker (local or remote)

---

# Installation on Cerbo GX

## 1. Enable SSH access

### GUI v2 (new interface)

1. Go to:
   ```
   Menu → Settings
   ```

2. Set access level:
   ```
   Settings → General → Access level → Superuser
   ```

3. Enable SSH:
   ```
   Settings → Services → SSH → Enabled
   ```

4. Find IP address:
   ```
   Settings → Network → Ethernet/WiFi
   ```

### GUI v1 (older firmware)

```
Settings → Services → SSH
```

---

## 2. Connect via SSH

```bash
ssh root@<cerbo-ip>
```

Default:

- User: `root`
- Password: (empty or your configured password)

---

## 3. Copy repository

```bash
scp -r victron_smartevse root@<cerbo-ip>:/data/
```

---

## 4. Build binary (on your PC)

```bash
./build.sh
```

Copy binary:

```bash
scp build/victron_smartevse root@<cerbo-ip>:/data/victron_smartevse/
```

Ensure executable:

```bash
chmod +x /data/victron_smartevse/victron_smartevse
```

---

## 5. Configure environment

Edit:

```bash
vi /data/victron_smartevse/smartevse.env
```

Example:

```env
MQTT_BROKER=tcp://127.0.0.1:1883
MQTT_USER=
MQTT_PASSWD=
LOG_FILE=/tmp/smartevse.log
# SMARTEVSE_IPS=192.168.1.50
```

---

## 6. Install service

```bash
cd /data/victron_smartevse
bash install.sh
```

---

## 7. Start service

```bash
bash restart.sh
```

Check status:

```bash
svstat /service/victron_smartevse
```

Check logs:

```bash
cat /tmp/smartevse.log
```

---

## SmartEVSE Configuration

### MQTT

Configure in SmartEVSE web interface:

- MQTT broker
- Username/password
- Topic prefix

Driver reads config via:

```
http://<smartevse-ip>/settings
```

---

### Required settings

- Enable MQTT
- Set **Mains meter = API**

---

## Cerbo GX Configuration

- MQTT broker must be reachable
- Default: `tcp://127.0.0.1:1883`
- No additional Victron MQTT config required

---

## Data Mapping

### SmartEVSE → Victron

| Topic | Description |
|------|------------|
| State | Charger state |
| Mode | Charging mode |
| EVCurrentL1-3 | Current per phase |
| EVChargePower | Power |
| EVEnergyCharged | Session energy |
| ESPTemp | Temperature |

---

### Victron → SmartEVSE

| Topic | Description |
|------|------------|
| Set/MainsMeter | Grid current |
| Set/HomeBatteryCurrent | Battery current |
| Set/Mode | Charger mode |

---

## Assumptions & Limitations

- Multiple SmartEVSE chargers supported (one D-Bus service per serial)
- Device instance allocated via `com.victronenergy.settings` (`ClassAndVrmInstance`)
- D-Bus sender hardcoded (`com.victronenergy.vebus.ttyS4`)
- MQTT topic prefix taken from SmartEVSE config
- Session time not implemented

---

## Use Case

This driver allows:

- SmartEVSE to use Victron grid + battery data
- Victron UI / VRM to display SmartEVSE

Especially useful for:

- Solar charging
- Battery-aware charging

---

## Troubleshooting

### Cannot connect via SSH

- Enable SSH in settings
- Check IP address
- Ensure network connectivity

### SmartEVSE not found

- Check network
- Use `SMARTEVSE_IPS`

### MQTT errors

- Verify broker settings
- Check SmartEVSE MQTT config

---

## Notes

- Install under `/data` (persistent storage)
- Service managed via `runit`
- Survives firmware updates

---

## Thanks

* Brian Akins who wrote [go-velib](https://github.com/bakins/go-velib)
* mr-manuel who wrote [venus-os_dbus-mqtt-ev-charger](https://github.com/mr-manuel/venus-os_dbus-mqtt-ev-charger)
