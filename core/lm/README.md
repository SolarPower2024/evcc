# Fork extensions: load management, battery and peak shaving

What this fork (SolarPower2024/evcc, branch `load-peak-features`) adds on top
of evcc. Everything is inert until configured, so an unconfigured installation
behaves exactly like evcc. Everything is set up in the ui, nothing in
`evcc.yaml`; Home Assistant is reached through the add-on's supervisor
connection.

Part 1 describes the features, part 2 how the fork is kept maintainable.

**Part 1: features**

1. [Load management priorities](#1-load-management-priorities)
2. [Battery in load management](#2-battery-in-load-management)
3. [Switch devices](#3-switch-devices)
4. [Heater in stages](#4-heater-in-stages)
5. [Shed guard](#5-shed-guard)
6. [Loads not following their limit](#6-loads-not-following-their-limit)
7. [Load management circuit and switch](#7-load-management-circuit-and-switch)
8. [Overview](#8-overview)
9. [Phase switching: 1p currents and delays](#9-phase-switching-1p-currents-and-delays)
10. [Battery grid charging by soc and one-time](#10-battery-grid-charging-by-soc-and-one-time)
11. [Peak shaving](#11-peak-shaving)
12. [Battery profiles](#12-battery-profiles)
13. [Home consumption forecast](#13-home-consumption-forecast)
14. [Battery identification](#14-battery-identification)
15. [Second feed-in tariff (EEG)](#15-second-feed-in-tariff-eeg)
16. [Optimizer inputs](#16-optimizer-inputs)
17. [Advanced settings](#17-advanced-settings)

**Part 2: maintenance**

- [Rules](#rules)
- [Upstream touch points](#upstream-touch-points)
- [Own files](#own-files)
- [Taking in a new evcc version](#taking-in-a-new-evcc-version)
- [Tests](#tests)

---

## 1. Load management priorities

evcc's circuits serve requests first come, first served. A priority puts an
order on that: **lower is shed first**. With all loads on the same priority
(the default) nothing changes compared to evcc.

One priority ranks everything: a loadpoint's regular priority decides pv
surplus and shedding alike. The battery has no evcc priority and keeps a value
of its own on the same 0-10 scale. All are set under *Lastmanagement-Details →
Prioritäten* by drag, top = highest; a drag renumbers the loads from the bottom
0, 1, 2 and so on, at most 10 (`assets/js/utils/lmPriorityOrder.ts`). A
loadpoint's value is its regular priority, also editable in the loadpoint
settings; the battery's is stored in `lmPriorities`. Older versions had a
separate load management priority per loadpoint; those values were taken over
into the regular priority once (`core/site_lm_priority.go`). An old `lmpriority`
key still in a stored loadpoint config is accepted (field `LmPrio_`), taken over
and then removed from the config at start (`dropOldLmPriority`).

How it works (`core/lm/lm.go`): a load whose request the circuit denies records
the denied amount as unserved demand. Loads with a lower priority get that
amount withheld from their own budget and give way on their next update, which
frees the power for the higher priority load one cycle later. A higher priority
load never exceeds the circuit limit, it only claims power that was freed. As
evcc updates one loadpoint per cycle, a full shed takes up to
`interval × number of loadpoints`. In an overload a load keeps what it draws
while the loads below it draw enough to cover the excess, so shedding starts at
the bottom.

Recovery uses the same reserve: freed power stays withheld from lower loads
while a higher one records unmet demand, so it goes up the ladder first, one
rung per cycle (`TestRecoveryFavoursHigherPriority`). A load that is off and not
asking holds no claim until its first request.

All state lives in one `lm.Manager` per site (`site.lmm()`); loadpoints reach it
through their site (`lp.lmm()`). The check runs inside evcc's `setLimit` through
`lmCircuit` (`core/loadpoint_lm.go`), which keeps evcc's calculation and adds
the priorities, switch devices and the shed guard.

## 2. Battery in load management

The home battery's grid charging takes part in load management once it is on a
circuit:

| Setting | Where | Default |
| --- | --- | --- |
| circuit the battery draws from | Lastmanagement-Details → Batterie-Stromkreis | none = not managed |
| priority | Lastmanagement-Details → Prioritäten | 0 |
| expected grid charge power | Lastmanagement-Details → Batterie-Netzladen | sum of the battery meters' max charge power |
| entity for the charge power | Lastmanagement-Details → Batterie-Netzladen | none = on/off charging |
| phases, wait after a shed, reservation expiry | Lastmanagement-Details → Erweitert | 3, 5 min, 10 min |

A battery switched through mode scripts is on or off, so the whole expected
charge power has to fit into the circuit. Without a known charge power, grid
charging on a circuit stays off and a warning is logged once. With a charge
power entity, evcc writes the grid charge power instead: the expected power,
trimmed to what fits below the peak limit and into the circuit, at least 500 W.
After a shed, grid charging waits for the hold-off, as stopping it frees exactly
the power that would let it start again. See `core/site_lm.go`.

## 3. Switch devices

A switch device (smart plug, heater switch) draws its full power or nothing.
evcc would switch a 3 kW heater on with 1.6 kW to spare, as that is still above
the minimum current, and the circuit would stay overloaded. Here it only
switches on when its whole power fits, against the circuit's power and current
limits alike. Its power is, in this order: the measurement while it draws, the
configured *Leistung* (template *Home Assistant Switch*, `ratedpower`), the last
measurement, the nominal maximum current on one phase. Without a power sensor
the configured power is also reported as its power while on. See
`core/loadpoint_lm.go` and `charger/switchsocket_lm.go`.

## 4. Heater in stages

A heater with one switch per stage, e.g. 3 × 3 kW switched per phase, runs as
one loadpoint (template *Home Assistant Heizstab in Stufen*: up to three
switches, power per stage, optional power sensor, delay before a higher
stage). The charger (`charger/switchstages.go`) reports one stage as minimum
and all as maximum power and switches on as many whole stages as fit. It is no
switch device, so load management steps it down stage by stage instead of
shedding it whole. Down is immediate, a higher stage waits until the last
change is the delay old (default 1 min), switching on from off follows the
loadpoint's enable delay. The loadpoint's phases must match the wiring. A
thermostat that cut out (draw up to the standby power, default 15 W) reports
ready and counts as 0 W. `TestStagesCircuitStepsDown`,
`TestStagesGiveWayToHigherPriority`.

## 5. Shed guard

*Lastmanagement-Details → Abwurfschutz*: a protected loadpoint that load
management switched off stays off for the set minutes (0 = off, up to 120), so
a heater does not flap around the limit. Only switching off a running load
counts: a switch losing its budget or a wallbox pushed below its minimum. While
held off it asks for nothing. Changes apply to a running guard right away. See
`core/lm/guard.go`, `core/site_lm_guard.go`.

## 6. Loads not following their limit

Shedding from the bottom relies on the lower loads giving way. Each cycle
compares what every load draws with what it was last allowed (the battery: its
grid charge limit). A load above it (tolerance 300 W or 10 %) on an overloaded
circuit counts a cycle; after the set cycles (*Erweitert*, default 3, 0 = off)
it is no longer counted on and the next load up is cut. It is counted on again
once it follows. See `core/lm/follow.go`, `core/site_lm_follow.go`.

## 7. Load management circuit and switch

*Erweitert → Stromkreis Lastmanagement (Peak)* names the circuit whose power
limit is the peak, beside one for the fuse or the agreed connection power.
Chosen, only it is shown in the overview, lifted by the switch and raised by
follow the peak; none = all circuits, nothing raised (`lmCircuit`).

*Mehr → Lastmanagement (Peak) → Lastmanagement* off lifts that circuit's power
limit (without one, all circuits') at runtime, so loads and battery grid
charging are no longer throttled for it. Fuse current limits, a HEMS limit
(§14a) and peak shaving keep applying; circuits whose limit comes from a plugin
are left alone. On restores the configured or followed limit. Survives a
restart (`lmOff`). See `core/site_lm_switch.go`.

## 8. Overview

*Mehr → Lastmanagement (Peak)* shows circuit load, peak shaving, battery grid
charging, every load with its state (running, throttled, shed and held off
until, waiting with what it needs and what is free, paused) and the last 20
events. Published as `lmStatus` at the end of every cycle
(`core/site_lm_status.go`, records in `core/lm/status.go`); it never feeds back
into decisions.

## 9. Phase switching: 1p currents and delays

A loadpoint with phase switching can have its own min and max current for 1p
(loadpoint settings, *Elektrik*); the regular range is then the 3p range and
empty 1p fields fall back to it. Same names, settings keys and config fields as
evcc PR 32505, so an evcc version can take the values over.

- scaling up needs the 1p maximum exhausted and the surplus at the 3p minimum;
  scaling down happens below the 3p minimum if the 1p minimum is reached, else
  the loadpoint disables as in evcc
- after a switch the limits of the new phase count apply right away
- fast charging and battery boost check the circuit with the 3p minimum
- without 1p values nothing changes (`TestCurrents1pInertWhenUnused`)

Two optional delays: how long the surplus has to allow 3p before scaling up and
be short of it before scaling down; empty = enable and disable delay as in
evcc. Starting and stopping charging keep those. The config takes them in ns,
as the ui sends a cleared field as "". See `core/loadpoint_phasecurrents.go`.

## 10. Battery grid charging by soc and one-time

*Netzladen nach Ladestand* (battery page): a switch with a start and a stop
soc, independent of evcc's price limit. Charging starts at or below the start
soc and runs until the stop soc, through evcc's battery mode path, so a Home
Assistant battery runs its `modeCharge` script; the battery's `maxsoc` still
applies. Survives a restart halfway.

*Einmalig laden* (battery page): charges once up to a soc and switches itself
off, right away or by a time at the cheapest slots before it (evcc's planner on
the planner tariff; right away once the time passed or the duration is
unknown). Cancelled when the battery is removed. See `core/site_lm_once.go`.

Both pass the same gate as price-based grid charging: the circuit
([2](#2-battery-in-load-management)) and a running peak
([11](#11-peak-shaving)).

```
POST   /api/batterysocgridcharge/{true|false}
POST   /api/batterysocgridchargestart/{soc}
POST   /api/batterysocgridchargestop/{soc}
POST   /api/batterygridchargeonce/{soc}[/{hh:mm}]
DELETE /api/batterygridchargeonce
```

## 11. Peak shaving

A capacity tariff bills the month's highest 15 minute average. The battery's
lower soc range is held back as a reserve for such peaks. Set on the battery
page (*Lastspitzenkappung*: on/off, limit, reserve) and under
*Lastmanagement-Details → Peak Shaving* (entity for the discharge setpoint,
energy counter, follow the peak).

Above the reserve the discharge controller gets the free value (default
10000 W) and the battery runs as usual. Below it, the battery only covers what
exceeds the allowed grid power: setpoint = `max(0, grid + battery − allowed)`.
The battery power is added back because the grid meter already reflects the
controller's own output; using the grid alone oscillates
(`TestPeakSetpointIsStable`). evcc only writes the setpoint to a number entity;
a Home Assistant automation does the discharge. A 2 % hysteresis keeps the soc
from flapping across the reserve.

The limit applies to the clock-aligned 15 minute window: `allowed` =
`(limit × 15 min − energy drawn so far) / time left`, so energy left unused
earlier allows more and a short spike is only covered when the window would end
above the limit. It is at most `cap × limit` (default 2), stops growing from the
freeze minute (default 12) as a meter clock off by seconds could move a late
draw into the next window, and the last cycle borrows the next window's
budget. What evcc did not see, after a start or a gap, counts at the limit.

The energy drawn comes from the grid meter's import counter, else a Home
Assistant energy sensor (kWh or Wh), else the grid power. A failed or backward
reading is replaced by the grid power for that interval; a counter standing
still while the grid power says more than 20 Wh in 2 minutes is replaced for
the rest of the window (`peakShavingSource`).

Below the reserve the battery is kept in normal mode, as hold would block the
discharge controller; a battery mode set from outside through the api stays.
While the demand without the battery exceeds the limit, grid charging pauses
and stays off for the hold-off, so it cannot add to the peak.

The window, the allowed power, the setpoint and the hysteresis are in
`core/peak`, which knows nothing about the site and has its own tests;
`core/site_peakshaving.go` feeds it and handles settings, Home Assistant and the
battery mode.

**Follow the peak**: once the month has a peak above the limit, shaving below it
saves nothing. Followed, the limit rises to the month's peak minus a buffer
(0-5 kW, default 0.5 kW) and never below the limit set by hand; a new month
starts there again. The load management circuit rises along. See
`core/site_peak_follow.go`.

**Statistics** (*Mehr → Peak Shaving*): per month the highest quarter hour with
and without the battery and how often it covered a peak; only quarter hours
metered from their start count, kept for 24 months (`peakMonths`,
`core/site_peak_stats.go`).

**Capacity tariff** (*Lastmanagement-Details → Leistungstarif*): price per kW
and year up to a threshold, a higher one above, at least a minimum and a share
of the agreed power; zero price = off. Each month's cost with and without the
battery and the saving are shown with the statistics. Prefilled with the
Austrian draft for 2027 (33.82 EUR/kW/year up to 10 kW, double above, at least
20 % of the agreed power and 2 kW). See `core/site_peak_tariff.go`.

## 12. Battery profiles

Named sets of settings (e.g. summer, winter) under *Lastmanagement-Details →
Profile*, picked on the battery page. A profile can hold soc grid charging, the
battery usage (priority, buffer and buffer start soc, discharge lock), peak
shaving (on/off, reserve, limit) and each wallbox's solar share; values not
ticked are left alone. Applied through the regular setters; priority, buffer
and buffer start soc are checked together and set bottom up, as evcc checks
each against the other two. Type in `core/lm/profile`, the rest in
`core/site_lm_profiles.go`.

```
POST   /api/lmprofile             create or update, json body
DELETE /api/lmprofile/{id}
POST   /api/lmprofile/{id}/apply
```

## 13. Home consumption forecast

*Erweitert → Verbrauchsprognose* (`homeForecast`): evcc forecasts the home base
load for the optimizer from the last 28 days per quarter hour.

- *Nach Wochentag* takes each day from the same weekday of the last 8 weeks; a
  weekday without complete data falls back to evcc's forecast
  (`core/site_load_weekday.go`).
- *Manuell* uses an uploaded csv load profile: average home power per quarter
  hour (or hour) per month, working days and weekends apart (`01-werktag`,
  `01-wochenende`) or together (`01`); missing day types take the other one,
  missing months the nearest. It is mixed with the measured energy of the last
  8 weeks (recent days count more, half-life 4 days): the profile scaled to the
  recent level, the recent profile per day type, weighted 0.5 while the profile
  fits and up to 0.9 the more it differs, and a deviation over 25 % in the last
  3 hours carries over and fades out. Without a usable profile evcc's forecast
  applies (`core/site_load_manual.go`, `POST/DELETE/GET /api/lmhomeprofile`).

*Sicherheitszuschlag Verbrauch* sets evcc's `profilePercentile`: a higher
percentile per quarter hour instead of the mean, for all of these profiles.

## 14. Battery identification

*Lastmanagement-Details → Batterie-Vermessung* learns the usable capacity and
round trip efficiency from evcc's stored 15 minute battery slots of the last 60
days: charging runs over at least 20 % soc give capacity / η, discharging runs
capacity × η; from the medians of at least 3 runs each, capacity = √(kc × kd)
and round trip = kd / kc. Runs with a soc jump, a gap or reverse flow are left
out. Plausible: 50-120 % of the configured capacity, 60-100 % round trip.
Refreshed every 6 hours. With *Gemessene Werte verwenden* the optimizer and
one-time grid charging use the measured values. See
`core/site_battery_ident.go`.

## 15. Second feed-in tariff (EEG)

Part of the export goes to an energy community at a fixed price, the rest gets
the regular feed-in tariff. Tariffs: *Einspeisevergütung EEG hinzufügen* below
the feed-in tariff (fixed price, 0 allowed); its card sets the Home Assistant
counter of the EEG export (kWh, Wh or MWh).

- the counter is recorded per 15 minute slot by a collector of group `meter`
  (`feedin-eeg`), which evcc keeps out of every balance; a changed counter
  starts a fresh recording
- the EEG price is stored per slot in `tariffs_eeg`
- `GET /api/feedinsplit?from&to&aggregate` returns per bucket: export, EEG,
  regular = export − EEG, and the revenue of both
- only counters are used; pv control, load management and peak shaving are
  untouched

The display on evcc's new energy page (evcc PR 33989) is prepared separately.
See `core/site_feedin_eeg.go`, `core/metrics/feedin_eeg_custom.go`.

## 16. Optimizer inputs

The optimizer plans battery and vehicle charging and today advises (soc
forecast, suggestions). The fork gives it its settings as inputs, so the plan
matches what the fork will do (`core/site_optimizer_lm.go`):

- peak shaving: the limit as hard grid import limit (`p_max_imp`), the reserve
  as minimum soc (`s_min`) in the first solve. Where that plan leaves peaks
  uncovered, or the battery is below the floor now, it is solved again
  (`core/site_optimizer_reserve_pass.go`) with the battery's own minimum and a
  floor per slot (`s_goal`) that follows the fork: lowered by the peaks it
  covers, raised by what really charges (pv surplus less what vehicles take,
  running and one-time grid charging), never by grid charging the optimizer
  only chooses. The floor is checked once more and solved a third time where it
  drifted.
- soc-based grid charging: the start soc as minimum; from the slot the battery
  reaches it the rest is solved again (`core/site_optimizer_soc_pass.go`) with
  the stop soc as goal, as the optimizer only knows minimums. While charging
  runs the stop soc is a goal of the first solve.
- grid charging goals follow the charge slot by slot as the fork does it, at the
  grid charge power with the charging efficiency; with peak shaving only with
  the room below the limit, paused while a peak runs.
- one-time grid charging: the target as goal at the chosen time, or right away.
- grid charging refused right now (shed hold-off, unknown charge power on a
  circuit) is not offered (`charge_from_grid`).
- load management: a loadpoint plans with at most its circuits' power,
  priorities 0-3/4-6/7-10 become `c_priority` 0/1/2.
- a price tariff set as planner tariff is the grid price the optimizer plans
  with (`p_N`); statistics and costs keep the grid tariff. With real prices
  close to the feed-in price the optimizer never discharges (stored energy is
  valued at least feed-in / 0.9, so discharging needs about 1.25 × feed-in); a
  fixed planning price fixes that, but also drives vehicle plans and smart cost
  limits.
- every further solve is checked (solved, complete, battery within its bounds,
  not more over the limit), otherwise the plan before stays.
- the plan is solved again when soc-based grid charging starts or stops, and a
  forced run arriving during a run follows right after it.

Without circuits, peak shaving and soc or one-time grid charging the request is
unchanged. The optimizer's automatic mode (evcc PR 32881) needs a gate at
execution, prepared separately on top of these inputs.

## 17. Advanced settings

*Lastmanagement-Details → Erweitert* (`POST /api/lmadvanced/{name}/{value}`,
`core/site_lm_advanced.go`). A value set there wins over the default; unset
values are not stored, so a changed default applies.

| Name | Setting | Default | Range |
| --- | --- | --- | --- |
| `hysteresis` | soc band of the peak reserve | 2 % | 0-20 |
| `freeValue` | setpoint for "discharge freely" | 10000 W | 1-100000 |
| `holdOff` | wait after battery grid charging was stopped | 5 min | 1-60 |
| `timeout` | expiry of unserved demand | 10 min | 1-60 |
| `phases` | battery phases for current accounting | 3 | 1-3 |
| `peakFreeze` | minute from which the peak budget no longer grows | 12 | 1-14 |
| `peakCap` | allowed grid power at most × limit | 2 | 1-10 |
| `followCycles` | cycles until a load not following is ignored | 3 | 0-20, 0 = off |
| `gridChargeWindow` | hours to reach the stop soc in the plan | 3 | 1-24 |
| `homeForecast` | home forecast: evcc, per weekday, manual | 0 | 0-2 |

Also there: the load management circuit ([7](#7-load-management-circuit-and-switch))
and evcc's `profilePercentile` ([13](#13-home-consumption-forecast)).

---

## Rules

Every fork feature follows these, so taking in a new evcc version stays cheap:

1. **Inputs, not overrides.** Feed settings into evcc's mechanisms (circuit
   checks, optimizer request, planner, regular setters) instead of overwriting
   its decisions. The one override, `updateBatteryModePeakAware`, keeps the
   battery in normal mode below the peak reserve.
2. **evcc first.** Check whether evcc has the capability or an open PR. When
   evcc ships an equivalent, switch to it and remove ours; where the result
   differs, decide explicitly.
3. **Own files, few hooks.** Logic lives in own files; evcc's files only get
   `// custom:` hook lines, all listed below. evcc's logic is called, not
   copied. In the ui, fork parts are own components mounted with one tag.
4. **Contract tests.** Each hook has a test pinning the evcc behaviour it relies
   on, and that the fork is inert while unused; `TestForkHooksInPlace` checks
   that every hook is still in place, see [Tests](#tests).

## Upstream touch points

Every change in an evcc file. Check these when merging a new evcc version.

| File | Change |
| --- | --- |
| `core/site.go` | `custom` field; `restoreCustom` in `restoreSettings`; `updateCustom` after `updatePower`; `setPeakGridEnergy` in `updateGridMeter`; `batteryGridChargeRequested` and `updateBatteryModePeakAware` in place of evcc's calls |
| `core/site_circuits.go` | `circuitLoads()` instead of `loadpointsAsCircuitDevices()` (adds the battery) |
| `core/site_load_predictor.go` | `homeProfileCustom` call in `homeProfile` |
| `core/site_optimizer.go` | `optimizerGridTariff` for the grid price, `applyLmOptimizerInputs` where the request is assembled, `lmOptimizerPasses` after the solve, `lmForecastLowest` for the forecast, `lmOptimizeLater`/`lmOptimizeAgain` in `optimizerUpdateAsync` |
| `core/site/api.go` | embeds `CustomAPI` |
| `core/loadpoint.go` | `loadpointCustom` field; `LmPrio_` field (old `lmpriority` key); `setLimit` checks against `lp.lmCircuit()` and calls `done`; two `lp.lmm().Peek*` probes; 1p currents: restore and publish calls, phase scaling (`pvScalePhases`, `pvMaxCurrent`, `fastChargingPhases`, `boostPower`) asks `effectiveMinCurrentFor`/`effectiveMaxCurrentFor`, the phase timers take `phaseScaleDelay` |
| `core/loadpoint_effective.go` | `effectiveMinCurrent`/`effectiveMaxCurrent` split per phase count (as in evcc PR 32505), min/max power use it |
| `core/loadpoint/config.go`, `server/http_config_loadpoint_handler.go` | `PhaseSwitchConfig` in the dynamic config, applied after min/max current, read back for the ui |
| `core/circuit/circuit.go` | over power logged via `overPowerLog()` (info, no ui notification) |
| `charger/switchsocket.go` | `RatedPower` config field |
| `templates/definition/charger/homeassistant-switch.yaml` | `ratedpower` parameter |
| `api/globalconfig/types.go`, `tariff/tariffs.go`, `cmd/setup.go`, `server/http_config_device_handler.go` | `feedInEeg` tariff: ref field, `Used`/`IsConfigured`, one `configureTariff` call, cleared on delete |
| `server/http.go` | `addCustomSiteRoutes`: a route colliding with an evcc route is left out and logged |
| `assets/js/views/App.vue` | mounts `LoadManagement/GlobalModals.vue` |
| `assets/js/views/Battery.vue` | mounts the battery cards and the profile selection |
| `assets/js/views/Config.vue` | *Lastmanagement-Details* section, `LmConfigModals.vue`, EEG tariff card and add button |
| `assets/js/components/BottomTabs/MoreMenu.vue` | mounts `LoadManagement/MoreMenuItems.vue` |
| `assets/js/components/Config/LoadpointModal.vue` | mounts `PhaseSwitchFields.vue`, 3-phase labels and minimum while it is shown |
| `assets/js/components/Config/TariffCard.vue`, `TariffModal.vue` | EEG counter in the EEG card, price templates for `feedInEeg`, planner price hint |
| `assets/js/components/Energyflow/Energyflow.vue` | "(Netzladen)" label |
| `assets/js/types/evcc.ts` | `State`/`ConfigLoadpoint` extend the types in `evcc-lm.ts`; `feedInEeg` tariff type |
| `i18n/de.json`, `i18n/en.json` | added texts only |

## Own files

| Area | Files |
| --- | --- |
| load management | `core/lm/` (`lm.go`, `guard.go`, `status.go`, `follow.go`), `core/loadpoint_lm.go`, `core/site_lm.go`, `core/site_lm_priority.go`, `core/site_lm_guard.go`, `core/site_lm_follow.go`, `core/site_lm_status.go`, `core/site_lm_switch.go`, `core/site_lm_advanced.go`, `core/circuit/circuit_custom.go` |
| switch devices, stages | `charger/switchsocket_lm.go`, `charger/switchstages.go`, `templates/definition/charger/homeassistant-stages.yaml` |
| phase switching | `core/loadpoint_phasecurrents.go`, `core/loadpoint/config_custom.go`, `core/keys/loadpoint_custom.go` |
| grid charging | `core/site_lm_once.go` (soc-based in `core/site_lm.go`) |
| peak shaving | `core/peak/`, `core/site_peakshaving.go`, `core/site_peak_follow.go`, `core/site_peak_stats.go`, `core/site_peak_tariff.go` |
| profiles | `core/lm/profile/`, `core/site_lm_profiles.go` |
| forecast | `core/site_load_weekday.go`, `core/site_load_manual.go`, `core/metrics/profile_custom.go` |
| battery identification | `core/site_battery_ident.go`, `core/metrics/slots_custom.go` |
| EEG | `core/site_feedin_eeg.go`, `core/metrics/feedin_eeg_custom.go` |
| optimizer | `core/site_optimizer_lm.go`, `core/site_optimizer_reserve_pass.go`, `core/site_optimizer_soc_pass.go` |
| api, keys | `core/site/api_custom.go`, `server/http_custom.go`, `core/keys/site_custom.go` |
| ui | `assets/js/types/evcc-lm.ts`, `assets/js/utils/lmPriorityOrder.ts`, `assets/js/components/LoadManagement/`, `assets/js/components/PeakShaving/`, the battery cards in `assets/js/components/Battery/` (`BatterySocGridChargeCard`, `BatteryGridChargeOnce`, `BatteryPeakShavingCard`, `BatteryProfileCard`, `ProfileIcon`), the config components in `assets/js/components/Config/` (`PeakShavingConfig`, `LmConfigModals` and its dialogs, `FeedInEegSummary`, `PhaseSwitchFields`) |
| build | `.github/workflows/custom-image.yml` |

## Taking in a new evcc version

Every Friday the issue *evcc-Update-Check* reviews the new evcc commits (see
[Tests](#tests)): what changed, which fork features evcc may now have itself,
which evcc commit causes which conflict, and whether the merge passes the
tests. With the secret `UPDATE_TOKEN` the PR *evcc-Update* (branch
`evcc-update` = evcc's master) is kept open as well; conflicts can then be
resolved on it and it is merged with a merge commit.

1. `git fetch upstream --tags` and merge evcc's **master** into
   `load-peak-features` (release branches only hold backports already in
   master). If the newest master does not build, merge the last commit that does.
2. Resolve conflicts with the touch point table above. Also check whether evcc
   changed the logic the fork builds on (circuits, battery mode, tariffs,
   optimizer, phase switching) where nothing conflicts.
3. Rule 2: look through the new evcc commits for functions equal or similar to
   the fork's; take evcc's where the result is the same and remove ours.
4. Push: the fork tests run on GitHub; then the live checks.
5. Never press GitHub's "Sync fork" on `load-peak-features`: on conflicts it
   offers "Discard commits".

## Tests

**Contract tests** pin what the hooks rely on and that the fork is inert while
unused:

- `TestForkHooksInPlace` parses every touched evcc file and checks that each
  hook from the table above is still there (Go: in the right function; ui: the
  mount). A hook lost in a merge fails here, while all other tests would stay
  green. A new hook gets a line in its table.
- `TestForkInertWhenUnused`, `TestEqualPrioritiesAreUpstream`,
  `TestCurrents1pInertWhenUnused`, `TestLmSMaxFromUpstream`: without
  configuration the fork changes nothing.
- `TestSetLimitUsesLmCircuit`, `TestPeakReserveKeepsExternalMode`,
  `TestLoadpointUsesSiteLoadManagement`, `TestMergeRoutesKeepsUpstream`: the
  hooks act on evcc's real control path.
- `TestRestoreCustomAfterRestart`: every fork setting survives a restart.
- `TestCustomRoutesMatch`: every api route reaches its handler; a new route
  needs a sample request there.

**Feature tests** live next to the code in `core`, `core/lm`, `core/peak`,
`core/lm/profile`, `core/circuit`, `core/metrics`, `core/loadpoint`, `charger`
and `server`; many replay logged situations. Two optimizer tests only run
against a running optimizer: `TestLmOptimizerScenarios` (`OPTIMIZER_URI`)
solves synthetic days and checks soc bounds, import limit, energy balance and
goals; `TestLmOptimizerReplay` (`OPTIMIZER_REPLAY`, `OPTIMIZER_URI`) replays a
recorded request.

Tests touching the settings store call `noSettingsDB` (keeps it in memory, as
evcc's `newDeleteTestSite` leaves its database open) and `keepSettings`
(restores it afterwards), see `core/fork_helpers_test.go`. A test opening a
database closes it again. The fork's tests pass in any order (`-shuffle=on`).

**On GitHub** (`.github/workflows/`, standard runners, free for a public
repository):

- `fork-tests.yml` on every push and pull request: build, vet, the whole Go
  testsuite (evcc's and the fork's), the race detector on the fork's packages,
  the ui checks (format, lint, types, i18n, vitest, build) and a shuffled run
  that reports but does not block.
- `custom-image.yml` builds an add-on image from a tag only after these passed.
- `upstream-check.yml` Fridays at noon (10:00 UTC): fetches evcc's master read
  only and runs the review `.github/upstream-check/review.mjs` into the issue
  *evcc-Update-Check*: a) the commits by area and the evcc files with fork
  hooks that changed, b) fork features evcc may now have itself (evcc PRs the
  fork waits for, names of the feature in evcc's added code, listed in
  `watch.json`; hints that need a review), c) each conflicting file with the
  evcc commit causing it (found with `git merge-tree`, commit by commit), d)
  the PRs to open. Without conflicts the merge is built and tested. With the
  secret `UPDATE_TOKEN` it also keeps the PR *evcc-Update* open. Never writes
  to evcc's repository; issue numbers in evcc's subjects become plain text
  ("evcc PR" and the number).

**Locally** the Go tests run in WSL (no Windows firewall prompts), the ui checks
in a checkout with `node_modules`. Live checks of a build with a simulated Home
Assistant and the ui checks with headless Edge stay local, as GitHub cannot
reach the test instances.
