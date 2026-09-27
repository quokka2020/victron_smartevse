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
- S2 Resource Manager stub endpoint at `/S2/0/Rm` for discovery, handshake advertisement, and message logging

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
tail -n 100 -F /data/log/victron_smartevse/current | tai64nlocal
# or if you specified a different log-file
cat <the value of LOG_FILE environment varaible in the smartevse.env>
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

### S2 stub (for DESS/Opportunity-load bootstrap)

Each exported EV charger service now also exposes `com.victronenergy.S2` on path `/S2/0/Rm` with stub transport methods:

- `Discover() -> true`
- `Connect(client_id, keepalive_s)`
- `KeepAlive(client_id)`
- `Message(client_id, payload)`
- `Disconnect(client_id)`

Current behavior is intentionally minimal:

- Advertises `Handshake` with supported version `0.8`
- Advertises schema-valid `Handshake` (`role=RM`, UUID message IDs, offered versions `0.0.2-beta` and `0.8`)
- Advertises schema-valid `ResourceManagerDetails` for an electricity-consuming EV charger with control types `NOT_CONTROLABLE` and `OPERATION_MODE_BASED_CONTROL`
- Waits for `HandshakeResponse` before emitting `ResourceManagerDetails` and any restored control state, which matches stricter clients such as Opportunity Loads
- Logs all incoming and outgoing S2 payloads
- Sends `ReceptionStatus(OK)` for incoming messages containing `message_id`
- Maintains `/S2/0/Active` (false on connect, true after successful handshake establishment, false on disconnect)
- Tracks selected protocol version and selected control type in endpoint `GetText()` state
- Accepts `SelectControlType`
- For `NOT_CONTROLABLE`, emits a schema-valid `PowerMeasurement` whose values reflect the current EV charger `/Ac/Power`, `/Ac/L1/Power`, `/Ac/L2/Power`, and `/Ac/L3/Power`
- For `OPERATION_MODE_BASED_CONTROL`, emits minimal schema-valid `OMBC.SystemDescription` and `OMBC.Status`
- OMBC transitions are advertised in both directions (`Idle -> Charge` and `Charge -> Idle`) so CEMs can both start and stop charging via mode switching
- Accepts `OMBC.Instruction`, logs it, validates the requested mode/factor, and emits an updated `OMBC.Status` to simulate mode switching
- Periodically republishes `PowerMeasurement` while `NOT_CONTROLABLE` is active
- Periodically republishes `OMBC.Status` while `OPERATION_MODE_BASED_CONTROL` is active
- Retains the last selected control type and simulated OMBC active mode across disconnect/reconnect, and re-advertises that state on the next connection
- Exposes Cerbo EVCS Opportunity Loads settings paths on the EV charger service:
  - `/S2/0/RmSettings/MaxChargePower` (writable, watt)
  - `/S2/0/RmSettings/RememberEvPhases` (writable, switch)

No real control logic is implemented yet.

---

### SmartEVSE → Victron

| Topic | Description |
|------|------------|
| State | Charger state |
| Mode | Strategy / pause state |
| EVCurrentL1-3 | Current per phase |
| CurrentOverride | External current control state |
| EVChargePower | Power |
| EVEnergyCharged | Session energy |
| ESPTemp | Temperature |

---

### Victron → SmartEVSE

| Topic | Description |
|------|------------|
| Set/MainsMeter | Grid current |
| Set/HomeBatteryCurrent | Battery current |
| Set/Mode | Strategy or `Pause` |
| Set/CurrentOverride | External current override |

### Mode / StartStop / AutoStart semantics

This bridge intentionally separates **managed mode** (Victron-facing intent) from **charging strategy** (SmartEVSE runtime behavior) and **charge permission**:

- Victron `/Mode=Manual` → SmartEVSE `Smart`
- Victron `/Mode=Auto` → SmartEVSE `Solar` by default
- Victron `/Mode=Auto` + active internal managed control → SmartEVSE `Smart`
- Victron `/Mode=Scheduled` → SmartEVSE `Solar` (SmartEVSE has no time schedules)
- Victron `/StartStop=Stop` → SmartEVSE `Pause`
- Victron `/StartStop=Start` → clears `Pause` and restores the current strategy
- Victron `/AutoStart=Enabled` → when an EV reconnects, clear `Pause`
- Victron `/AutoStart=Disabled` → keep `Pause` until `/StartStop=Start`

This makes Victron `Auto` usable with Opportunity Loads / DynamicESS: when an internal controller owned by this driver is active, the bridge keeps Victron in `Auto` while temporarily driving SmartEVSE in `Smart` mode. When that internal controller is inactive, `Auto` falls back to SmartEVSE `Solar`. 

---

## Assumptions & Limitations

- Multiple SmartEVSE chargers supported (one D-Bus service per serial)
- Device instance allocated via `com.victronenergy.settings` (`ClassAndVrmInstance`)
- Victron `/AutoStart` and `/Position` are persisted per charger under `com.victronenergy.settings/Settings/Devices/<devicename>/...`
- D-Bus sender hardcoded (`com.victronenergy.vebus.ttyS4`)
- MQTT topic prefix taken from SmartEVSE config
- `AutoStart` is bridge-local, persisted across restarts
- `Scheduled` mode has no time schedule behind it: SmartEVSE exposes only `Smart`, `Solar`, and `Pause`, so it behaves as `Solar`
- `/Mode` is not persisted: after a restart it is derived from the SmartEVSE mode again, so `Scheduled` comes back as `Auto`

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
* Koen de Laat for his contribution to this repo