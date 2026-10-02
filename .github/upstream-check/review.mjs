// Review of what evcc changed since the fork's last merge, read only: nothing
// is merged or written here. Prints a markdown report (German, for the
// issue and the update PR) and writes a json summary for the workflow.
//
// usage: node review.mjs <upstream ref> <summary.json>
//
// a) what changed, grouped by component
// b) fork features evcc may now have itself (watched evcc PRs merged, names
//    or words of the feature in evcc's added code), see watch.json
// c) which evcc commits cause merge conflicts, found commit by commit
// d) the PRs to open: the merge, and a review per feature found in b)
//
// No issue numbers, mentions or links of evcc's repository end up in the
// text: issue numbers become plain "evcc PR" and the number, so nothing
// refers back to it.

import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";

const [upstream = "upstream/master", summaryFile = "/tmp/summary.json"] = process.argv.slice(2);
const watch = JSON.parse(readFileSync(new URL("./watch.json", import.meta.url), "utf8"));

const git = (...args) =>
	execFileSync("git", args, { encoding: "utf8", maxBuffer: 1 << 28 }).trimEnd();
const gitStatus = (...args) => {
	try {
		return { code: 0, out: git(...args) };
	} catch (e) {
		return { code: e.status, out: String(e.stdout || "").trimEnd() };
	}
};

const clean = (s) =>
	s
		.replace(/\(#(\d+)\)/g, "(evcc PR $1)")
		.replace(/#(\d+)/g, "evcc PR $1")
		.replace(/@/g, "(at)")
		.replace(/https?:\/\/\S+/g, "")
		.replace(/evcc-io\//g, "");

const base = git("merge-base", "HEAD", upstream);
const commits = git("log", "--reverse", "--format=%H%x09%s", `HEAD..${upstream}`)
	.split("\n")
	.filter(Boolean)
	.map((l) => {
		const [sha, subject] = l.split("\t");
		const pr = (subject.match(/\(#(\d+)\)\s*$/) || [])[1];
		return { sha, subject, pr: pr ? Number(pr) : undefined };
	});

const out = [];
const line = (s = "") => out.push(s);
const summary = { count: commits.length, conflicts: [], features: [] };

line(`Stand ${new Date().toISOString().slice(0, 16).replace("T", " ")} UTC, automatisch erstellt.`);
line();
line(`**Neue evcc-Commits seit dem letzten Merge:** ${commits.length}`);

if (!commits.length) {
	line();
	line("Nichts zu tun.");
	writeFileSync(summaryFile, JSON.stringify(summary));
	console.log(out.join("\n"));
	process.exit(0);
}

// a) what changed
const groups = new Map();
for (const c of commits) {
	const m = c.subject.match(/^([^:]{1,40}):\s/);
	const key = m ? m[1].replace(/\s*\(.*\)$/, "") : "Sonstiges";
	if (!groups.has(key)) groups.set(key, []);
	groups.get(key).push(c);
}

const forkTouched = git("diff", "--name-only", "--diff-filter=M", base, "HEAD")
	.split("\n")
	.filter(Boolean);
const evccChanged = new Set(git("diff", "--name-only", base, upstream).split("\n").filter(Boolean));
const hookFilesChanged = forkTouched.filter((f) => evccChanged.has(f));

line();
line("## a) Was sich geändert hat");
line();
line(`${evccChanged.size} Dateien in ${commits.length} Commits, nach Bereich:`);
line();
for (const [key, list] of [...groups].sort((a, b) => b[1].length - a[1].length)) {
	line(`<details><summary>${clean(key)} (${list.length})</summary>`);
	line();
	for (const c of list) line(`- ${clean(c.subject)}`);
	line();
	line("</details>");
}
line();
line("**Geänderte evcc-Dateien, in denen der Fork Hooks hat:**");
line(hookFilesChanged.length ? hookFilesChanged.map((f) => `- \`${f}\``).join("\n") : "keine");

// b) fork features evcc may now have itself
const added = new Map(); // file -> added lines
let file;
for (const l of git(
	"diff",
	"-U0",
	base,
	upstream,
	"--",
	".",
	":(exclude)*_test.go",
	":(exclude)*.test.ts",
	":(exclude)i18n/*",
	":(exclude)vendor/*"
).split("\n")) {
	if (l.startsWith("+++ ")) {
		file = l.startsWith("+++ b/") ? l.slice(6) : undefined;
		if (file && !added.has(file)) added.set(file, []);
	} else if (file && l.startsWith("+") && !l.startsWith("+++")) {
		added.get(file).push(l.slice(1).trim());
	}
}

line();
line("## b) evcc-Funktionen, die der Fork selbst gebaut hat");
line();
line(
	"Hinweise aus der Beobachtungsliste (`.github/upstream-check/watch.json`), keine Beweise: jeder Treffer braucht ein Review."
);

const prsInUpdate = new Map(commits.filter((c) => c.pr).map((c) => [c.pr, c]));
for (const f of watch.features) {
	const merged = (f.prs || []).filter((n) => prsInUpdate.has(n)).map((n) => prsInUpdate.get(n));
	const hits = [];
	for (const [path, lines] of added) {
		if (f.paths && !f.paths.some((p) => path.startsWith(p))) continue;
		const found = lines.filter((l) => (f.keywords || []).some((k) => l.includes(k)));
		if (found.length) hits.push({ path, n: found.length, example: found[0].slice(0, 120) });
	}
	if (!merged.length && !hits.length) continue;

	summary.features.push({
		name: f.name,
		files: f.files,
		merged: merged.map((c) => c.pr),
		hits: hits.map((h) => h.path),
	});
	line();
	line(`**${f.name}**`);
	for (const c of merged) line(`- evcc hat einen beobachteten PR übernommen: ${clean(c.subject)}`);
	for (const h of hits.slice(0, 6))
		line(`- \`${h.path}\`: ${h.n} Treffer, z. B. \`${clean(h.example).replace(/`/g, "'")}\``);
	if (hits.length > 6) line(`- … ${hits.length - 6} weitere Dateien`);
	line(`- Fork-Dateien: ${f.files.map((p) => `\`${p}\``).join(", ")}`);
}
if (!summary.features.length) {
	line();
	line("Keine Treffer.");
}

// c) conflicts, and which evcc commit causes each
const all = gitStatus(
	"merge-tree",
	"--write-tree",
	"--name-only",
	"--no-messages",
	"HEAD",
	upstream
);
const conflicts = all.code === 1 ? all.out.split("\n").slice(1).filter(Boolean) : [];
summary.conflicts = conflicts;

line();
line("## c) Konflikte beim Merge");
line();
if (!conflicts.length) {
	line("Keine, der Merge geht ohne Konflikt.");
} else {
	const culprit = new Map();
	const relevant = commits.filter((c) =>
		git("diff-tree", "--no-commit-id", "--name-only", "-r", c.sha)
			.split("\n")
			.some((p) => conflicts.includes(p))
	);
	for (const c of relevant) {
		const r = gitStatus(
			"merge-tree",
			"--write-tree",
			"--name-only",
			"--no-messages",
			"HEAD",
			c.sha
		);
		if (r.code !== 1) continue;
		for (const p of r.out.split("\n").slice(1)) if (p && !culprit.has(p)) culprit.set(p, c);
	}
	for (const p of conflicts) {
		const c = culprit.get(p);
		line(
			`- \`${p}\`: ${c ? `ausgelöst durch ${clean(c.subject)}` : "Auslöser nicht eindeutig (erst im Zusammenspiel mehrerer Commits)"}`
		);
	}
}

// d) PRs to open
line();
line("## d) Vorgeschlagene PRs");
line();
line(
	`- **Merge der ${commits.length} evcc-Commits**${conflicts.length ? `, Konflikte lösen in ${conflicts.map((p) => `\`${p}\``).join(", ")}` : ""}; danach Regel 2 (evcc-Code übernehmen, wo er dasselbe leistet) und alle Tests.`
);
for (const f of summary.features) {
	line(
		`- **Rückbau/Umbau prüfen: ${f.name}**: evcc-Lösung mit der eigenen vergleichen; bei gleichem Ergebnis auf evcc umstellen und eigenen Code entfernen, bei Unterschieden entscheidest du.`
	);
}

writeFileSync(summaryFile, JSON.stringify(summary));
console.log(out.join("\n"));
