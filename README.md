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
- S2 Resource Manager at `/S2/0/Rm`: Dynamic ESS / Opportunity Loads can control charging via OMBC (Standby + per-ampere charge modes with min-on/min-off timers)

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
# S2_DEBUG=1   # log full S2 message payloads
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

### S2 Resource Manager (Dynamic ESS / Opportunity Loads)

Each EV charger service also exposes `com.victronenergy.S2` on `/S2/0/Rm`, using Victron's S2-over-D-Bus transport (`Discover`, `Connect`, `KeepAlive`, `Message`, `Disconnect`, plus the `Message`/`Disconnect` signals). Victron CEMs (Dynamic ESS, Opportunity Loads) use it to control the charger with **Operation Mode Based Control (OMBC)**.

**Session**

- Handshake on protocol `0.0.2-beta`. `ResourceManagerDetails` is sent after `HandshakeResponse`. It advertises `NOT_CONTROLABLE` and `OPERATION_MODE_BASED_CONTROL`, L1–L3 power measurements and an instruction processing delay of 2 s.
- `/S2/0/Active` is false on connect, true after the handshake and false on disconnect.
- One CEM at a time: a `Connect` from a second client is rejected. `KeepAlive` or `Message` from an unknown client is answered with a `Disconnect` signal.
- Watchdog: when a CEM is silent for more than 2.5× its keep-alive interval, the session is dropped (`Disconnect` signal "KeepAlive missed") and the charger is released.
- `PowerMeasurement` (L1–L3 from `/Ac/Lx/Power`) is sent every 5 s.

**Operation modes**

- `Standby` (0 W) plus one **fixed-power mode per ampere**, from `MinCurrent` up to `MaxCurrent`. The top step is also capped by `/S2/0/RmSettings/MaxChargePower`.
- Power ranges are advertised per active phase at 230 V nominal, e.g. `10A` on 3 phases is 3 × 2300 W.
- The step size widens automatically when there would be more than 31 steps. This keeps the full-mesh transition list within the S2 limit of 1000. The top step is always included.
- Fixed-power modes are used because Dynamic ESS always sends `operation_mode_factor = 1.0`: it picks the mode whose summed power is closest to its target. A single variable-range mode would therefore always charge at maximum. Factors between 0 and 1 are accepted but ignored, and `OMBC.Status` always reports factor 1.0.
- Every mode can transition to every other mode. Changing between charge steps is free. Switching on and off is protected by **min-on / min-off timers of 1 minute**:
  - `Standby → step` starts min-on and is blocked by min-off.
  - `step → Standby` starts min-off and is blocked by min-on.
  - A blocked instruction gets `ReceptionStatus TEMPORARY_ERROR` unless it has `abnormal_condition=true`.
- `OMBC.TimerStatus` is sent to clients that support it. Dynamic ESS does not, so it is skipped for `dynamic_ess`.
- On (re)selection of OMBC, the initial mode follows the charger: the nearest step if it is charging, otherwise `Standby`.
- The `SystemDescription` is re-sent when phases, current limits or `MaxChargePower` change. An active step that disappears is moved to the nearest remaining step.

**Charger control**

S2 only controls the charger while Victron `/Mode` is `Auto`. Instructions are rejected with `TEMPORARY_ERROR` in other modes.

| Operation mode | SmartEVSE action |
|---|---|
| `Standby` | `Set/Mode Pause` |
| `<n>A` | `Set/CurrentOverride <n*10>` + `Set/Mode Smart` (Victron `/Mode` stays `Auto`) |

- `/GxAutoMode/ChargingCurrent` shows the current requested by S2 (0 in Standby or when not controlled).
- `/StartStop=Stop` by the user wins: S2 charge requests are not applied until `/StartStop=Start`.
- On plug-in in Auto mode, the charger pauses and reports Standby to an active S2 controller, which can then issue a new charge instruction. If the previous S2 mode was charging, the min-off timer applies. The S2 session stays connected; Dynamic ESS may resend a stale setpoint if its own schedule state has not been refreshed.
- AutoStart continues to control plug-in behavior in Manual mode. In Auto mode, the charger starts paused regardless of AutoStart, including when no S2 controller is connected.
- **Release**: when the CEM disconnects, the keep-alive watchdog fires, the CEM selects `NOT_CONTROLABLE`, or `/Mode` leaves `Auto`, the override is cleared. A pause caused by S2 is lifted, and the charger returns to normal `Auto` (SmartEVSE `Solar`). Returning to `Auto` while a CEM is connected re-applies the active S2 mode.

**Phases**

- The number of phases used by the EV is detected from the measured `EVCurrentL1-3` (> 1 A) once stable for 20 s.
- Until detection, the EV is assumed to charge on one phase, regardless of the charger's phase configuration. Actual charging can initially exceed the S2 advertised power for a multi-phase EV; the model updates after detection.
- On unplug, phases reset to one unless `/S2/0/RmSettings/RememberEvPhases` is set. This setting retains the detected count across unplug events during the bridge's current runtime, not across bridge restarts.

**Settings** (persisted in `com.victronenergy.settings/Settings/Devices/<devicename>/`)

- `/S2/0/RmSettings/MaxChargePower` (W, writable; 0 = no extra cap) → `S2MaxChargePower`
- `/S2/0/RmSettings/RememberEvPhases` (switch, writable) → `S2RememberEvPhases`

**Logging**

By default S2 traffic is logged as one-line summaries. `PowerMeasurement` messages and `ReceptionStatus` messages with status OK are not logged at all, and the `SystemDescription` is shown only as a mode and transition count. Set `S2_DEBUG=1` in `smartevse.env` to log full payloads.

**Limitations**

- Phase switching (1P/3P via `EnableC2`) is not driven by S2.
- With a large current range the full-mesh `SystemDescription` is large (≈170 KB for 6–32 A). Validate it on the GX with the venus-s2-tools before relying on it.
- Dynamic ESS re-sends a blocked instruction every 5 s while a timer runs, which shows up as `TEMPORARY_ERROR` lines in the log.

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
- Victron `/Mode`, `/AutoStart` and `/Position` are persisted per charger under `com.victronenergy.settings/Settings/Devices/<devicename>/...`. On first registration, `/Mode` is inferred from the SmartEVSE strategy.
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