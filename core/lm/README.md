# Load management extensions

Custom additions on top of upstream evcc's circuits. Everything here is inert
until configured, so an unconfigured installation behaves exactly like upstream.

## 1. Priority-based shedding

Upstream circuits serve requests first come, first served. A shed priority puts
an order on that: **lower is shed first**, the default `0` puts every load on
the same level.

One priority ranks everything: a loadpoint's regular (upstream) `priority`
decides pv surplus, shedding and, once planned charging shares circuit
capacity, the planner alike. The battery has no upstream priority; it keeps a
value of its own on the same 0-10 scale.

All of them are set in the ui under *Lastmanagement-Details → Prioritäten*, for
every loadpoint on a circuit and for the home battery once it is assigned to
one. A loadpoint's value there is its regular priority (also editable in the
loadpoint settings), the battery's is stored in the `lmPriorities` setting,
with `loadmanagement.battery.priority` in yaml as fallback.

Earlier the loadpoints had a separate `lmpriority`. Those values (from the ui,
else a non-zero yaml `lmpriority`) are taken over into the loadpoints'
priority once, logged, see `core/site_lm_priority.go`. That changes the pv
surplus order accordingly.

### How it works

A load whose request the circuit denies records the denied amount as unserved
demand. Loads with a lower priority then get that amount withheld from their own
budget and give way on their next update, which frees the power for the
higher-priority load one cycle later.

The higher-priority load never gets to exceed the circuit limit - it only claims
power that has actually been freed. Because evcc updates one loadpoint per cycle,
a full shed takes up to `interval x number of loadpoints`.

Recovery is not a separate mechanism and there is no restore pass in reverse
order. The same reserve does both jobs: power that frees up stays withheld from
the lower-priority loads for as long as a higher-priority load records unmet
demand, so it goes up the priority ladder first. Only once nobody above is short
does the reserve fall to zero and the lower-priority loads take the rest back.
Each rung of that ladder costs one cycle. `TestRecoveryFavoursHigherPriority`
walks through it.

Two caveats on the way back up:

- A load that is off and not asking records no demand, so it does not hold a
  claim on freed capacity. It registers one on the first cycle it does ask,
  which is one cycle before it is served.
- The battery's `holdoff` deliberately breaks the ordering: after being shed it
  waits out the timer even if capacity frees up earlier, because stopping it
  frees exactly the power that would make it start again.

## 2. Battery in load management

The home battery's grid charging power counts against a circuit and is switched
off when the budget runs out. A battery driven through mode scripts can only be
on or off, so the full expected charge power has to fit.

```yaml
site:
  loadmanagement:
    timeout: 10m # unserved demand expiry, must outlast a full round-robin
    battery:
      circuit: main # the circuit the battery draws from, empty = not managed
      priority: 0 # shed before everything else
      power: 5000 # expected grid charge power in W, 0 = sum of maxchargepower
      phases: 3 # for current accounting
      holdoff: 5m # wait before retrying after a shed
```

`power` falls back to the sum of the battery meters' `maxchargepower`. Without
either, the battery is not gated and a warning is logged once.

The `holdoff` prevents flapping: stopping the battery frees exactly the power
that made it start again.

## 3. Soc-based grid charging

A switch plus a start and a stop soc, independent of the price-based
`batteryGridChargeLimit`. Configured in the UI under **Hausbatterie**, persisted
in settings, and also available via the api:

```
POST /api/batterysocgridcharge/{true|false}
POST /api/batterysocgridchargestart/{soc}
POST /api/batterysocgridchargestop/{soc}
```

Charging starts once the soc is at or below the start value and continues until
the stop value is reached. It goes through the same battery mode path as
price-based grid charging, so a Home Assistant battery triggers its `modeCharge`
script as usual. The battery's own `maxsoc` still applies on top as a hard
ceiling.

## Maintenance rules

Every fork feature follows these, so that taking in a new evcc version stays cheap:

1. **Inputs, not overrides.** Feed our settings into upstream mechanisms (circuit
   checks, optimizer request, planner, regular setters) instead of overwriting
   upstream decisions afterwards. An unavoidable override stays in one named hook
   with the reason next to it. The only one today: `updateBatteryModePeakAware`
   keeps the battery in normal mode below the peak reserve.
2. **Upstream first.** Check whether evcc has the capability or an open PR for it.
   When upstream ships an equivalent, switch to it and remove ours.
3. **Own files, few hooks.** Logic lives in own files. Upstream files only get
   `// custom:` hook lines, all listed below. Upstream logic is called, not copied.
4. **Contract tests.** Each hook has a test pinning the upstream behaviour it
   relies on, and that the fork is inert while its features are off:
   `TestForkInertWhenUnused`, `TestSetLimitUsesLmCircuit`,
   `TestEqualPrioritiesAreUpstream`, `TestPeakReserveKeepsExternalMode`,
   `TestFinalizeFeedIn` (upstream session and metrics models).

## Upstream touch points

Keep these in mind when merging a new evcc version:

| File | Change |
| --- | --- |
| `core/site.go` | `lm` import, `LoadManagement`/`loadMgmt`/`peakShaving` fields, two restore calls, `batteryGridChargeRequested`, `updatePeakShaving`, `updateFeedInFinalization`, `updateBatteryModePeakAware`, `setPeakGridEnergy` in `updateGridMeter` |
| `core/site_circuits.go` | `circuitLoads()` instead of `loadpointsAsCircuitDevices()` |
| `core/loadpoint.go` | `lm` import, `LmPrio` field (yaml fallback), `setLimit` checks against `lp.lmCircuit()` instead of `lp.circuit` (upstream calculation unchanged) and calls `done`, two `lm.Peek*` probes |
| `charger/switchsocket.go` | `RatedPower` config field, stands in for a missing power sensor |
| `templates/definition/charger/homeassistant-switch.yaml` | `ratedpower` parameter |
| `core/site/api.go` | embeds `CustomAPI`, one line |
| `api/globalconfig/types.go`, `tariff/tariffs.go`, `cmd/setup.go`, `server/http_config_device_handler.go` | `feedInEeg` tariff role: ref field, `Used`/`IsConfigured`, one `configureTariff` call, cleared on delete |
| `assets/js/components/Config/TariffModal.vue` | `feedInEeg` offers the price templates |
| `core/site_optimizer.go` | `applyLmOptimizerInputs` where the optimizer request is assembled |
| `server/http.go` | merges `customSiteRoutes`, one loop |
| `assets/js/views/Battery.vue` | mounts the new cards, profile selection at the bottom |
| `assets/js/views/Config.vue` | load management details section and its modals, OeMAG modal |
| `assets/js/views/App.vue` | mounts the load management overview and the peak statistics |
| `assets/js/components/BottomTabs/MoreMenu.vue` | "Lastmanagement" and "Peak Shaving" entries |
| `assets/js/components/Config/TariffCard.vue` | OeMAG summary in the feed-in card |
| `assets/js/components/Energyflow/Energyflow.vue` | "(Netzladen)" label |
| `assets/js/types/evcc.ts`, `i18n/de.json`, `i18n/en.json` | state fields and texts |

Everything else lives in files of its own: `core/lm/`, `core/site_lm.go`, `core/site_lm_guard.go`,
`core/site_lm_advanced.go`, `core/site_lm_status.go`, `core/site_lm_profiles.go`, `core/site_lm_follow.go`,
`core/site_peak_stats.go`, `assets/js/components/LoadManagement/`, `assets/js/components/PeakShaving/`,
`core/site_peakshaving.go`, `core/loadpoint_lm.go`, `charger/switchsocket_lm.go`, `core/keys/site_custom.go`,
`core/site/api_custom.go`, `server/http_custom.go`, `core/site_feedin.go`, `core/metrics/tariffs_custom.go`, `core/site_optimizer_lm.go`, `core/site_lm_once.go`, `core/site_lm_priority.go`, `core/site_feedin_eeg.go`, `core/metrics/feedin_eeg_custom.go`,
`tariff/oemag.go`, `tariff/wrapper_custom.go`, `templates/definition/tariff/oemag.yaml` and the new Vue
components.

## Shed guard

A loadpoint that load management had to switch off can be held off for a set
time, so a heater does not flap while the demand hovers around the limit. The
minutes (0 = off, up to 120) and the protected loadpoints are set under
Lastmanagement-Details → Abwurfschutz:

```
POST /api/lmshedguard/{minutes}
POST /api/lmshedprotect/{loadpoint}/{true|false}
```

Only switching off a running load counts as a shed: a switch that loses its
whole budget, or a wallbox pushed below its minimum current. A load that could
not start for lack of power is not held off. While held off, the loadpoint asks
for nothing, so lower priority loads may use the power. Changing the minutes or
the protection applies to a running guard right away. The guard is in
`core/lm/guard.go`, applied in `lmCircuit` (`core/loadpoint_lm.go`), the settings
in `core/site_lm_guard.go`.

## Advanced settings

Hysteresis, free value, grid charge hold-off, reservation expiry, battery
phases, the peak budget's freeze minute and cap and the cycles for loads not
following their limit are set under Lastmanagement-Details → Erweitert
(`POST /api/lmadvanced/{name}/{value}`). A value set there overrides the yaml
value, which overrides the default. See `core/site_lm_advanced.go`.

## Loads not following their limit

In an overload a load keeps its power while the loads below it could free the
excess. That only works if they give way. Each cycle `lm.CheckFollowing` compares
what every load draws with what it was last allowed (`lm.Record`, for the battery
its grid charge limit). A load above its limit (tolerance 300W or 10%) on an
overloaded circuit counts a cycle; after the set cycles (Erweitert, default 3,
0 = off) it is no longer counted on, so the next load up the priority order is
cut. It is counted on again once it draws what it was allowed. See
`core/lm/follow.go` and `core/site_lm_follow.go`.

## Battery profiles

Named sets of settings, e.g. summer and winter, set up under
Lastmanagement-Details → Profile and picked on the battery page. A profile can
hold soc grid charging (on/off, start, stop), battery usage (priority, buffer and
buffer start soc, discharge lock in fast and planned charging), peak shaving
(on/off, reserve, limit) and the solar share of each wallbox. Values not ticked
are left alone. Applying goes through the regular setters; priority, buffer and
buffer start soc are checked as a combination first and then set bottom up,
since evcc checks each against the other two. The type is in
`core/lm/profile` (no dependencies, so the site api can use it), the rest in
`core/site_lm_profiles.go`.

```
POST   /api/lmprofile             create or update, json body
DELETE /api/lmprofile/{id}
POST   /api/lmprofile/{id}/apply
```

## Overview

Mehr → Lastmanagement shows what load management is doing: circuit load, peak
shaving, battery grid charging, every load with its state (running, throttled,
shed and held off until, waiting with what it needs and what is free, paused)
and the last 20 events. `core/lm/status.go` records each load's last request
and the events, `core/site_lm_status.go` publishes `lmStatus` at the end of
every cycle (from `updateBatteryModePeakAware`). Nothing in there feeds back
into the decisions.

## Peak statistics

Mehr → Peak Shaving shows, per month, the highest quarter hour average with the
battery (grid draw) and without it (grid draw plus battery power, charging
counts negative), and how often the battery started covering a peak. Only
quarter hours metered from their start count. Kept for 24 months in
`peakMonths`, see `core/site_peak_stats.go`.

## Second feed-in tariff (EEG)

Part of the export can go to an energy community (EEG) at a fixed price, the rest
gets the standard feed-in tariff (OeMAG). Tariff settings: "Einspeisevergütung EEG
hinzufügen" below the feed-in tariff (fixed price, 0 allowed), its card sets the
Home Assistant counter of the EEG export (kWh, Wh or MWh).

- The counter is recorded per 15 minute slot by a collector of group `meter`
  (`feedin-eeg`), which upstream keeps out of every balance. A changed counter
  starts a fresh recording, so the jump between two counters never counts.
- The EEG price is persisted per slot in `tariffs_eeg`.
- `GET /api/feedinsplit?from&to&aggregate` returns per bucket: export (grid
  meter), EEG (counter), standard = export minus EEG (clamped at 0), and the
  revenue of both, priced slot by slot.
- Only counters are used. The grid meter power that drives PV control, load
  management and peak shaving is untouched; self-consumption and the solar
  share of sessions stay valued at the standard feed-in tariff.
- The display on the new energy page (evcc PR 33989, not released yet) is
  prepared separately; until then the data is recorded and available via the
  api.

Without a counter nothing runs and evcc behaves as upstream.

## Optimizer

The optimizer plans battery and vehicle charging and, today, advises: its
result is the battery soc forecast and the suggestions. The fork gives it its
settings as inputs, so the plan matches what the fork will actually do, see
`core/site_optimizer_lm.go`:

- peak shaving: peak limit as hard grid import limit (`p_max_imp`), reserve
  as the home battery's minimum soc (`s_min`)
- soc-based grid charging: start soc as minimum soc, so the charging is
  planned ahead before the battery would fall below it; while it runs the
  stop soc as goal (`s_goal`) within the grid charge window (*Erweitert →
  Netzlade-Ziel erreichen in*, default 3 h)
- one-time grid charging: its target as goal, right away or at the chosen time
- grid charging refused right now (shed hold-off, running peak, unknown charge
  power on a circuit) is not offered (`charge_from_grid`)
- load management: a loadpoint plans with at most its circuits' power, the
  priorities 0-3/4-6/7-10 become `c_priority` 0/1/2

Without circuits, peak shaving, soc-based and one-time grid charging the
request is unchanged. The optimizer's automatic mode (evcc PR 32881, not
released yet) additionally needs a gate at execution; that is prepared
separately on top of these inputs.

`TestLmOptimizerReplay` sends a recorded request with these inputs to a
running optimizer (`OPTIMIZER_REPLAY`, `OPTIMIZER_URI`, optional
`REPLAY_SETTINGS`, `REPLAY_GRID_PRICE`, `REPLAY_SOC`) and checks the plan.

## One-time grid charging

Battery page, below *Netzladen nach Ladestand*: grid-charges once up to the
chosen soc and switches itself off, right away or by a time of day at the
cheapest slots before it (upstream planner on the planner tariff, right away
once the time passed or when the duration is unknown). It survives a restart,
can be cancelled and passes the same gate as the soc-based grid charging, see
`core/site_lm_once.go`.

## 4. Peak shaving

See `core/site_peakshaving.go`. The battery's lower soc range is reserved for
grid demand peaks; above the reserve the controller is told it may discharge
freely. The setpoint is `max(0, gridPower + batteryPower - allowed)`. The battery
power is added back because the grid meter already reflects the controller's own
output, and using it directly oscillates. `TestPeakSetpointIsStable` pins that
down.

The limit applies to the average of the clock-aligned 15 minute window, which is
what the demand charge is billed on. `allowed` is the grid power that keeps the
window's average at the limit: `(limit × 15 min − energy drawn so far) / time
left`. Energy left unused earlier allows more, so a short spike is only covered
when the window as a whole would end above the limit. Three bounds:

- `allowed` is at most `cap × limit` (default 2).
- From the freeze minute on (default 12) it no longer grows, only falls: a
  meter clock off by a few seconds could otherwise move a large late draw into
  the next window.
- The last cycle of a window reaches into the next one, so the time left is at
  least 30s, filled up with the next window's budget.

What evcc did not see, after a start or a gap over 2 minutes at a window
boundary, counts at the limit.

The energy drawn in the window comes from, in this order:

1. the grid meter's import counter, when the meter has one
2. a Home Assistant energy sensor (kWh or Wh), set under Lastmanagement-Details
   → Peak Shaving (`POST /api/peakshavingenergyentity/{entity}`)
3. the grid power of each cycle

A counter reading that fails, or goes backwards, is replaced by the grid power
for that interval. A counter that stands still is taken as late at first; once
the grid power says more than 20Wh were drawn over more than 2 minutes, the
grid power is used for the rest of the window. `peakShavingSource` shows which
one is in use.

While the reserve is held, the battery is forced into normal mode and grid
charging is blocked, since charging from the grid would create the peak.
