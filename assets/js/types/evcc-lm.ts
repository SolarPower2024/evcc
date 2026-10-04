// Custom extension: the types of this fork's state and config fields, kept out
// of evcc.ts so upstream changes there merge without conflicts. evcc.ts extends
// State and ConfigLoadpoint with them and re-exports everything.

// a load taking part in load management, see core/site_lm.go
export interface LmPriority {
  /** Config name, e.g. db:3, or "battery". */
  name: string;
  /** Loadpoint title, empty for the battery. */
  title: string;
  /** Shed priority 0-10, lower is shed first. */
  priority: number;
  battery?: boolean;
}

// advanced load management settings, see core/site_lm_advanced.go
export interface LmHomeProfile {
  /** File name of the upload. */
  name: string;
  /** Time of the upload. */
  uploaded: string;
  /** Months the file gave, 1..12, the others take the nearest. */
  months: number[];
  /** Average kWh per day by month. */
  daily: number[];
}

export interface LmAdvanced {
  /** Peak reserve soc band in %. */
  hysteresis: number;
  /** Setpoint signalling free discharge in W. */
  freeValue: number;
  /** Battery grid charge hold-off in minutes. */
  holdOff: number;
  /** Unserved demand expiry in minutes. */
  timeout: number;
  /** Battery phases for current accounting. */
  phases: number;
  /** Minute of the quarter hour from which the peak budget no longer grows. */
  peakFreeze: number;
  /** Allowed grid draw at most this multiple of the peak limit. */
  peakCap: number;
  /** Home consumption forecast per weekday. */
  homeWeekday?: boolean;
  /** Home consumption forecast: 0 evcc, 1 per weekday, 2 from the uploaded load profile. */
  homeForecast?: number;
  /** Cycles after which a load ignoring its limit is no longer counted on, 0 = off. */
  followCycles: number;
  gridChargeWindow: number;
}

// a battery profile, see core/lm/profile. Values left out are not changed.
export interface LmProfile {
  id: string;
  name: string;
  icon?: string;
  gridCharge?: boolean;
  gridChargeStart?: number;
  gridChargeStop?: number;
  prioritySoc?: number;
  bufferSoc?: number;
  bufferStartSoc?: number;
  dischargeControl?: boolean;
  peakShaving?: boolean;
  peakReserve?: number;
  /** In W. */
  peakLimit?: number;
  /** Solar share in % by loadpoint name. */
  solarShare?: Record<string, number>;
}

// one month of peak statistics, see core/site_peak_stats.go
export interface PeakMonth {
  /** YYYY-MM */
  month: string;
  /** Highest quarter hour average of the grid draw in W. */
  peak: number;
  peakAt: string;
  /** The same without the battery. */
  demand: number;
  demandAt: string;
  /** Peaks the battery covered. */
  interventions: number;
}

export interface PeakFollow {
  enabled: boolean;
  /** W below the month's peak. */
  buffer: number;
  /** W, the limit set by hand. */
  base: number;
}

export interface PeakTariffMonth {
  /** YYYY-MM */
  month: string;
  /** kW billed. */
  billed: number;
  /** Capacity cost of the month with the battery. */
  cost: number;
  /** The same without the battery. */
  costWithout: number;
  /** costWithout - cost, negative when grid charging raised the peak. */
  saving: number;
}

export interface PeakTariff {
  /** Per kW and year up to the threshold, 0 = off. */
  price: number;
  threshold: number;
  priceAbove: number;
  agreed: number;
  minShare: number;
  minimum: number;
  months: PeakTariffMonth[];
}

// load management overview, see core/site_lm_status.go
export interface LmLoadStatus {
  name: string;
  title: string;
  battery?: boolean;
  priority: number;
  /** Protected by the shed guard. */
  protected?: boolean;
  /** Drawn now in W. */
  power: number;
  state: "running" | "throttled" | "shed" | "waiting" | "paused" | "off";
  /** Asked for in W. */
  requested?: number;
  /** Allowed in W. */
  allowed?: number;
  /** Shed or paused until. */
  until?: string;
}

export interface LmEvent {
  at: string;
  type: "shed" | "throttled" | "gridChargePaused" | "gridChargeDenied" | "peak";
  load?: string;
  a: number;
  b: number;
}

export interface LmStatus {
  loads: LmLoadStatus[];
  /** Newest first. */
  events: LmEvent[];
}

/** State fields added by this fork. */
export interface LmState {
  /** Soc-based grid charging of the home battery is enabled. */
  batterySocGridCharge?: boolean;
  /** Soc in % at or below which soc-based grid charging starts. */
  batterySocGridChargeStart?: number;
  /** Soc in % at or above which soc-based grid charging stops. */
  batterySocGridChargeStop?: number;
  /** One-time grid charging up to target soc, right away or by until. */
  batteryGridChargeOnce?: { target: number; until?: string | null; active?: boolean };
  /** Battery peak shaving is enabled. */
  peakShaving?: boolean;
  /** Grid peak limit in W the battery reserve is used to stay below. */
  peakShavingLimit?: number;
  /** Soc in % below which the battery is reserved for demand peaks. */
  peakShavingReserve?: number;
  /** Assumed grid charge power in W entered by the user, 0 = derived. */
  peakShavingChargePower?: number;
  /** Grid charge power in W actually used by the peak check, 0 = not determinable. */
  peakShavingChargePowerEffective?: number;
  /** Where the effective charge power came from: setting, config, meter or unknown. */
  peakShavingChargePowerSource?: string;
  /** Battery reserve is currently being held for peaks. */
  peakShavingActive?: boolean;
  /** Battery power in W currently requested by peak shaving, or the free-discharge signal. */
  peakShavingPower?: number;
  /** Home Assistant number entity receiving the peak shaving setpoint. */
  peakShavingEntity?: string;
  /** Average grid power in W of the running 15 minute metering window. */
  peakShavingWindowAvg?: number;
  /** Grid power that keeps the window's average at the limit, in W. */
  peakShavingAllowed?: number;
  /** End of the running 15 minute window. */
  peakShavingWindowEnd?: string;
  /** Where the window's energy comes from. */
  peakShavingSource?: "meter" | "entity" | "power";
  /** Home Assistant grid import counter, used when the grid meter has none. */
  peakShavingEnergyEntity?: string;
  /** Monthly peak statistics, newest first. */
  peakMonths?: PeakMonth[];
  /** Follow the peak: the limit rises to the month's peak minus the buffer (W), base = limit set by hand. */
  peakFollow?: PeakFollow;
  /** Load management switch: off lifts the circuits' power limits; limits = configured values of changed circuits. */
  /** circuit: the load management (peak) circuit, empty = all */
  lmOff?: {
    enabled: boolean;
    limits: Record<string, number>;
    dynamic: string[];
    circuit: string;
  };
  /** Capacity tariff with each month's cost with and without the battery, a zero price is off. */
  peakTariff?: PeakTariff;
  /** Battery capacity and efficiency learned from the stored slots. */
  batteryIdent?: {
    use: boolean;
    updated: string;
    batteries: {
      name: string;
      title: string;
      /** kWh, datasheet */
      configured: number;
      /** kWh measured, 0 = not yet */
      capacity: number;
      /** round trip 0..1, 0 = not yet */
      efficiency: number;
      charges: number;
      discharges: number;
      valid: boolean;
    }[];
  };
  /** Home Assistant number entity receiving the grid charge power, empty = on/off charging. */
  peakShavingChargeEntity?: string;
  /** Grid charge power in W currently written to that entity, 0 = not charging. */
  peakShavingChargeSetpoint?: number;
  /** Loads taking part in load management with their shed priority, lower is shed first. */
  lmPriorities?: LmPriority[];
  /** Minutes a protected loadpoint stays off after load management shed it, 0 = off. */
  lmShedGuard?: number;
  /** Config names of the loadpoints the shed guard protects. */
  lmShedProtected?: string[];
  /** Advanced load management settings in effect. */
  lmAdvanced?: LmAdvanced;
  /** Uploaded load profile for the home consumption forecast, null = none. */
  lmHomeProfile?: LmHomeProfile | null;
  /** Home Assistant counter of the export under the second feed-in tariff (EEG), empty = off. */
  feedInEegEntity?: string;
  /** Current price of the second feed-in tariff. */
  tariffFeedInEeg?: number;
  /** Load management overview, only while circuits are configured. */
  lmStatus?: LmStatus;
  /** Battery profiles. */
  lmProfiles?: LmProfile[];
  /** Id of the profile applied last, empty = none. */
  lmProfileActive?: string;
  /** Loadpoints a profile can set the solar share of, heating devices left out. */
  lmProfileWallboxes?: { name: string; title: string; solarShare: number }[];
}

/** Loadpoint config fields added by this fork, see core/loadpoint/config_custom.go. */
export interface LmConfigLoadpoint {
  minCurrent1p?: number; // 1p current limits, 0/empty = minCurrent
  maxCurrent1p?: number; // 1p current limits, 0/empty = maxCurrent
  phaseScale3pDelay?: number; // ns before scaling up, 0/empty = enable delay
  phaseScale1pDelay?: number; // ns before scaling down, 0/empty = disable delay
}
