# Load management extensions

Custom additions on top of upstream evcc's circuits. Everything here is inert
until configured, so an unconfigured installation behaves exactly like upstream.

## 1. Priority-based shedding

Upstream circuits serve requests first come, first served. `lmpriority` on a
loadpoint puts an order on that: **lower is shed first**, the default `0` puts
every load on the same level.

```yaml
loadpoints:
  - title: Wallbox
    charger: wallbox
    circuit: main
    priority: 5 # pv surplus goes here first (upstream, unchanged)
    lmpriority: 1 # ... but this is the first load to be reduced

  - title: Heizstab
    charger: ha-switch-heater
    circuit: main
    lmpriority: 2

  - title: Wärmepumpe
    charger: ha-switch-heatpump
    circuit: main
    lmpriority: 5 # keeps its power the longest
```

`lmpriority` is deliberately separate from `priority`: the load that should get
pv surplus first is usually not the one that should keep power when the fuse is
the constraint.

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

## Upstream touch points

Keep these in mind when merging a new evcc version:

| File | Change |
| --- | --- |
| `core/site.go` | `lm` import, `LoadManagement`/`loadMgmt` fields, `restoreLmSettings()` call, `batteryGridChargeRequested` |
| `core/site_circuits.go` | `circuitLoads()` instead of `loadpointsAsCircuitDevices()` |
| `core/loadpoint.go` | `lm` import, `LmPriority_` field, four `lm.Validate*`/`lm.Peek*` calls |
| `core/keys/site.go` | three soc grid charge keys |
| `core/site/api.go` | six soc grid charge getters/setters |
| `server/http.go` | three routes |
| `assets/js/views/Battery.vue` | mounts `BatterySocGridChargeCard` |
| `assets/js/types/evcc.ts`, `i18n/de.json`, `i18n/en.json` | state fields and texts |

Everything else lives in `core/lm/`, `core/site_lm.go`, `core/loadpoint_lm.go`
and `assets/js/components/Battery/BatterySocGridChargeCard.vue`.
