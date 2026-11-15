// The feed-in deletion of Konfiguration → System → Debug (DebugModal.vue) goes
// through api/db and needs the login and the admin password.

/** Without a login (e.g. the add-on's ingress frame, which holds no cookie) the request cannot succeed: say so before sending it. */
export function loginMissing(loggedIn: boolean, authDisabled: boolean): boolean {
  return !authDisabled && !loggedIn;
}

export type FeedInAnswer = "password" | "error" | "ok";

/** What an answer of the api means; any 200 is a success, whatever the login looks like now. */
export function feedInAnswer(status: number): FeedInAnswer {
  if (status === 401 || status === 403) return "password";
  if (status >= 400) return "error";
  return "ok";
}

export type FeedInSection = "feedIn" | "feedInRecalc";

/** The sections that work on a day range with a preview and the admin password. */
export function isFeedInSection(name: string): name is FeedInSection {
  return name === "feedIn" || name === "feedInRecalc";
}

/** The api call of a section: the preview reads, the second step deletes (feed-in) or posts (recalculation). */
export function feedInRequest(
  name: FeedInSection,
  confirmed: boolean
): { method: "get" | "post" | "delete"; url: string } {
  if (name === "feedInRecalc") {
    return { method: confirmed ? "post" : "get", url: "/db/metrics/feedinrecalc" };
  }
  return { method: confirmed ? "delete" : "get", url: "/db/metrics/feedin" };
}
