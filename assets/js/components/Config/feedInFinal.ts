// Custom extension: helpers for the OeMAG finalization, see core/site_feedin.go

/** First day of a YYYY-MM month, local time. */
export function monthDate(month: string): Date {
  const [year, m] = month.split("-").map(Number);
  return new Date(year ?? 1970, (m ?? 1) - 1, 1);
}

/** YYYY-MM of a date. */
export function monthKey(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}`;
}

/**
 * The day the next automatic finalization runs: this month's finalize day while
 * the previous month is still open, else next month's. Past the day with the
 * previous month still open, it is due right now.
 */
export function feedInNextFinalization(now: Date, day: number, lastDone?: string): Date {
  const previous = monthKey(new Date(now.getFullYear(), now.getMonth() - 1, 1));
  const thisMonth = new Date(now.getFullYear(), now.getMonth(), day);

  if (!lastDone || lastDone < previous) {
    return now.getDate() >= day ? now : thisMonth;
  }
  return new Date(now.getFullYear(), now.getMonth() + 1, day);
}

/** A market price in EUR/kWh as ct/kWh with the three decimals OeMAG publishes. */
export function fmtMarketPrice(eur: number, locale?: string): string {
  const ct = new Intl.NumberFormat(locale, {
    minimumFractionDigits: 3,
    maximumFractionDigits: 3,
  }).format(eur * 100);
  return `${ct} ct/kWh`;
}
