package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forkHooks lists every hook the fork places in evcc's own files: the function
// and the names it has to reference. Kept in step with the touch point table
// in core/lm/README.md.
var forkHooks = []struct {
	file, fn string // fn: "Func" or "Recv.Func"
	names    []string
}{
	{"core/site.go", "Site.restoreSettings", []string{"restoreCustom"}},
	{"core/site.go", "Site.updateGridMeter", []string{"setPeakGridEnergy", "powerRead"}},
	{"core/site.go", "Site.collectMeters", []string{"setPeakMeterRead"}},
	{"core/site.go", "Site.update", []string{"updateCustom", "batteryGridChargeRequested", "updateBatteryModePeakAware"}},
	{"core/site.go", "Site.updateLoadpoints", []string{"meteredPower"}},
	{"core/site_circuits.go", "Site.updateCircuits", []string{"circuitLoads"}},
	{"core/site_load_predictor.go", "Site.homeProfile", []string{"homeProfileCustom"}},
	{"core/site_optimizer.go", "Site.optimizerUpdateAsync", []string{"lmOptimizeLater", "lmOptimizeAgain"}},
	{"core/site_optimizer.go", "Site.optimizerRequest", []string{"optimizerGridTariff", "applyLmOptimizerInputs"}},
	{"core/site_optimizer.go", "Site.optimizerUpdate", []string{"lmOptimizerResult"}},
	{"core/site_optimizer.go", "Site.addBatteryForecastTotals", []string{"lmForecastLowest"}},
	{"core/loadpoint.go", "Loadpoint.restoreSettings", []string{"restorePhaseSwitch"}},
	{"core/loadpoint.go", "Loadpoint.Prepare", []string{"publishPhaseSwitch", "publishStages"}},
	{"core/loadpoint.go", "Loadpoint.UpdateChargePowerAndCurrents", []string{"publishChargePower"}},
	{"core/loadpoint.go", "Loadpoint.publishChargeProgress", []string{"meteredPower"}},
	{"core/loadpoint.go", "Loadpoint.Update", []string{"meteredPower"}},
	{"core/loadpoint.go", "Loadpoint.setLimit", []string{"lmCircuit", "done"}},
	{"core/loadpoint.go", "Loadpoint.circuitAllowsPhases", []string{"lmm", "PeekPower"}},
	{"core/loadpoint.go", "Loadpoint.fastChargingPhases", []string{"effectiveMinCurrentFor", "phaseScaleDelay"}},
	{"core/loadpoint.go", "Loadpoint.pvScalePhases", []string{"lmm", "PeekCurrent", "min1pCurrentOr", "effectiveMinCurrentFor", "phaseScaleDelay", "phaseScaleDelay1p"}},
	{"core/loadpoint.go", "Loadpoint.batterySupported", []string{"projectPhaseSwitch1p"}},
	{"core/loadpoint.go", "Loadpoint.boostPower", []string{"effectiveMinCurrentFor"}},
	{"core/loadpoint.go", "Loadpoint.scalePhases", []string{"minCurrentScaleUp"}},
	{"core/loadpoint.go", "Loadpoint.pvMaxCurrent", []string{"effectiveMinCurrentFor", "effectiveMaxCurrentFor", "projectPhaseSwitch1p", "holdStage"}},
	{"core/loadpoint_effective.go", "Loadpoint.effectiveMinCurrent", []string{"effectiveMinCurrentFor", "currents1pPhases"}},
	{"core/loadpoint_effective.go", "Loadpoint.effectiveMaxCurrent", []string{"effectiveMaxCurrentFor", "currents1pPhases"}},
	{"core/loadpoint_effective.go", "Loadpoint.EffectiveMinPower", []string{"effectiveMinCurrentFor"}},
	{"core/loadpoint_effective.go", "Loadpoint.effectiveMaxPower", []string{"effectiveMaxCurrentFor"}},
	{"core/loadpoint/config.go", "DynamicConfig.Apply", []string{"applyPhaseSwitch"}},
	{"core/loadpoint/config.go", "SplitConfig", []string{"withoutDroppedKeys"}},
	{"core/circuit/circuit.go", "Circuit.Update", []string{"overPowerLog"}},
	{"server/http.go", "HTTPd.RegisterSiteHandlers", []string{"addCustomSiteRoutes"}},
	{"server/http.go", "HTTPd.RegisterSystemHandler", []string{"setCustomAuth"}},
	{"server/http_config_loadpoint_handler.go", "getLoadpointDynamicConfig", []string{"PhaseSwitchConfigOf"}},
	{"server/http_config_helper.go", "testInstance", []string{"customTestResults"}},
	{"server/http_config_device_handler.go", "cleanupTariffRef", []string{"FeedInEeg"}},
	{"cmd/setup.go", "configureTariffs", []string{"FeedInEeg"}},
	{"api/globalconfig/types.go", "TariffRefs.IsConfigured", []string{"FeedInEeg"}},
	{"api/globalconfig/types.go", "TariffRefs.Used", []string{"FeedInEeg"}},
	{"charger/switchsocket.go", "NewSwitchSocketFromConfig", []string{"RatedPower"}},
	{"util/log.go", "newLogger", []string{"Output"}},
	{"cmd/root.go", "runRoot", []string{"CloseFile", "forkUpdateCheck"}},
}

// forkDeferHooks lists the hooks that have to run on every way out of an evcc
// function: a deferred call in the function body, before any return of evcc's
// own. A call at the end would be skipped by the first return added above it.
var forkDeferHooks = []struct {
	file, fn string
	name     string
}{
	{"core/site.go", "Site.restoreSettings", "restoreCustom"},
}

// forkUiHooks lists the fork's mounts in evcc's ui files
var forkUiHooks = []struct {
	file string
	text []string
}{
	{"assets/js/views/App.vue", []string{"<LmGlobalModals"}},
	{"assets/js/views/Config.vue", []string{"<PeakShavingConfig", "<LmConfigModals", "feedInEegTariff"}},
	{"assets/js/views/Battery.vue", []string{"<!-- custom: soc grid charging and peak shaving", "<BatterySocGridChargeCard", "<BatteryPeakShavingCard", "<BatteryProfileCard"}},
	{"assets/js/components/BottomTabs/MoreMenu.vue", []string{"<LmMoreMenuItems"}},
	{"assets/js/components/Config/CircuitModal.vue", []string{"<CircuitPeakHint", "CircuitPeakHint,"}},
	{"assets/js/components/Config/LoadpointModal.vue", []string{"<PhaseSwitchFields", "emptyUnsetPhaseSwitch(this.values)", "config.loadpoint.minCurrent${showCurrent1p ? '3p' : ''}Label", "chargerIsStages"}},
	{"assets/js/components/Energyflow/Energyflow.vue", []string{"batteryChargeLabelKey(this.batteryChargeHold"}},
	{"assets/js/components/Loadpoints/Loadpoint.vue", []string{"chargerStages: Boolean", "const estimate = fmtPowerEstimate(", `:value="chargePower || chargePowerEstimate"`}},
	{"assets/js/components/Site/Site.vue", []string{"loadpoints: measuredLoadpoints(this.enabledLoadpoints)"}},
	{"assets/js/components/Loadpoints/Mode.vue", []string{"chargerStages: Boolean", "this.switchDevice || this.chargerStages"}},
	{"assets/js/views/Forecast.vue", []string{"<SnowCoverSwitch>", "form-check form-switch mt-2 mb-0\"", "SnowCoverSwitch, // custom"}},
	{"assets/js/views/Log.vue", []string{"<LogFileButton", "LogFileButton,"}},
	{"assets/js/components/Config/DeviceTags.vue", []string{"case \"stages\": // custom"}},
	{"assets/js/components/Config/TariffCard.vue", []string{"<FeedInEegSummary"}},
	{"assets/js/components/Config/TariffModal.vue", []string{"feedInEeg", "tariff-planner-optimizer"}},
	{"assets/js/views/Energy.vue", []string{"gridChartSeries", ":feed-in-eeg=\"feedInEeg\"", "loadFeedInSplit(requestKey)", "withoutFeedInEeg(", "splitLegends(list, this.feedInEeg"}},
	{"assets/js/components/Energy/GroupChart.vue", []string{"s.returnColor ||"}},
	{"assets/js/components/Energy/GridStats.vue", []string{"feedInEegStats(this.feedInEeg"}},
	{"assets/js/types/evcc.ts", []string{"extends LmState", "extends LmConfigLoadpoint", `export type * from "./evcc-lm"`}},
}

// referenced returns the names a function's body refers to
func referenced(t *testing.T, file, fn string) map[string]bool {
	t.Helper()

	res := make(map[string]bool)
	ast.Inspect(funcBody(t, file, fn), func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			res[id.Name] = true
		}
		return true
	})

	return res
}

// funcBody returns the body of a function
func funcBody(t *testing.T, file, fn string) *ast.BlockStmt {
	t.Helper()

	path := filepath.Join("..", filepath.FromSlash(file))
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)

	recv, name, ok := strings.Cut(fn, ".")
	if !ok {
		recv, name = "", fn
	}

	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name || fd.Body == nil {
			continue
		}

		if (recv == "") != (fd.Recv == nil) {
			continue
		}
		if recv != "" && !strings.HasSuffix(recvName(fd.Recv.List[0].Type), recv) {
			continue
		}

		return fd.Body
	}

	require.Failf(t, "function not found", "%s in %s", fn, file)
	return nil
}

// recvName returns a receiver type's name, e.g. Site for *Site
func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// returns reports whether a statement contains a return of its function
func returns(st ast.Stmt) bool {
	found := false

	ast.Inspect(st, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false // returns from its own function
		case *ast.ReturnStmt:
			found = true
		}
		return !found
	})

	return found
}

// isTestingGuard reports whether a statement is "if testing.Testing() {...}"
func isTestingGuard(st ast.Stmt) bool {
	ifs, ok := st.(*ast.IfStmt)
	if !ok {
		return false
	}

	found := false
	ast.Inspect(ifs.Cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Testing" {
			found = true
		}
		return !found
	})

	return found
}

// TestForkDeferHooks fails when a hook that has to run on every way out of an evcc
// function is no deferred call of the function body any more, or an evcc return
// stands before it: restoreSettings returns on the first failing restore step of
// evcc, and nothing of the fork would be loaded (peak shaving off, no circuit, the
// battery held by evcc never handed back).
func TestForkDeferHooks(t *testing.T) {
	for _, h := range forkDeferHooks {
		t.Run(h.file+" "+h.fn, func(t *testing.T) {
			body := funcBody(t, h.file, h.fn)

			at := -1
			for i, st := range body.List {
				d, ok := st.(*ast.DeferStmt)
				if !ok {
					continue
				}
				if sel, ok := d.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == h.name {
					at = i
					break
				}
			}
			require.GreaterOrEqual(t, at, 0, "%s: %s is no deferred call of the function body", h.fn, h.name)

			// only the guard for tests may return before it
			for _, st := range body.List[:at] {
				if !isTestingGuard(st) {
					assert.False(t, returns(st), "%s: a return of evcc stands before the deferred %s", h.fn, h.name)
				}
			}
		})
	}
}

// TestForkHooksInPlace fails when an evcc update or a merge conflict dropped a
// hook: evcc's own tests and the fork's tests of its functions would both stay
// green, while the function is simply no longer called.
func TestForkHooksInPlace(t *testing.T) {
	for _, h := range forkHooks {
		t.Run(h.file+" "+h.fn, func(t *testing.T) {
			refs := referenced(t, h.file, h.fn)
			for _, n := range h.names {
				assert.True(t, refs[n], "%s no longer refers to %s", h.fn, n)
			}
		})
	}

	for _, h := range forkUiHooks {
		t.Run(h.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(h.file)))
			require.NoError(t, err)
			for _, s := range h.text {
				assert.Contains(t, string(b), s)
			}
		})
	}
}

// callsMethod reports whether a node contains a call of the method name
func callsMethod(n ast.Node, name string) bool {
	found := false

	ast.Inspect(n, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
				found = true
			}
		}
		return !found
	})

	return found
}

// flatStmtIndex returns the index of the statement of the list that is a plain
// statement (no if, for, switch or block) and calls the method, or -1
func flatStmtIndex(list []ast.Stmt, name string) int {
	for i, st := range list {
		switch st.(type) {
		case *ast.ExprStmt, *ast.AssignStmt, *ast.DeclStmt:
			if callsMethod(st, name) {
				return i
			}
		}
	}
	return -1
}

// TestForkUpdateOrder fails when the fork's calls in Site.update moved: updateCustom
// runs once the meters are read and the power is calculated, and only then, the
// battery calls of the fork replace evcc's. Peak shaving's safeguard for a failing
// grid meter hangs on this order.
func TestForkUpdateOrder(t *testing.T) {
	body := funcBody(t, "core/site.go", "Site.update")

	// (1) updateCustom in the else block of the meter read, after updatePower
	var meters *ast.IfStmt
	for _, st := range body.List {
		if ifs, ok := st.(*ast.IfStmt); ok && ifs.Init != nil && callsMethod(ifs.Init, "updateMeters") {
			meters = ifs
		}
	}
	require.NotNil(t, meters, "no if with updateMeters")

	els, ok := meters.Else.(*ast.BlockStmt)
	require.True(t, ok, "the meter read has no else block")

	power := flatStmtIndex(els.List, "updatePower")
	custom := flatStmtIndex(els.List, "updateCustom")
	require.GreaterOrEqual(t, power, 0, "updatePower is no statement of the else block")
	require.GreaterOrEqual(t, custom, 0, "updateCustom is no statement of the else block")
	assert.Greater(t, custom, power, "updateCustom has to follow updatePower")

	// (3) a failing meter read goes on with the battery handling
	assert.False(t, returns(meters.Body), "the error branch of the meter read must not return")

	// (2) the battery calls of the fork are plain statements of the function, in order
	charge := flatStmtIndex(body.List, "batteryGridChargeRequested")
	mode := flatStmtIndex(body.List, "updateBatteryModePeakAware")
	require.GreaterOrEqual(t, charge, 0, "batteryGridChargeRequested is no statement of the function level")
	require.GreaterOrEqual(t, mode, 0, "updateBatteryModePeakAware is no statement of the function level")
	assert.Greater(t, mode, charge)

	// (4) evcc's originals are not called any more
	for _, n := range []string{"batteryGridChargeActive", "updateBatteryMode"} {
		assert.False(t, callsMethod(body, n), "Site.update calls evcc's %s again", n)
	}
}

// mentions reports whether a node contains the identifier (a field name of a
// literal, a selector's field, a variable)
func mentions(n ast.Node, name string) bool {
	found := false

	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})

	return found
}

// TestForkOptimizerRequestOrder fails when applyLmOptimizerInputs moved inside
// optimizerRequest: it reads the series evcc built (Ft per slot, the circuit's
// PMaxImp) and changes them, and it hands the batteries over before evcc moves
// them into the request. No test runs optimizerRequest (it needs tariffs, home
// profile, loadpoints and meters), so the order is checked in the syntax tree.
func TestForkOptimizerRequestOrder(t *testing.T) {
	body := funcBody(t, "core/site_optimizer.go", "Site.optimizerRequest")

	build, limit, hook, move := -1, -1, -1, -1
	for i, st := range body.List {
		switch s := st.(type) {
		case *ast.AssignStmt:
			if mentions(s, "OptimizationInput") && mentions(s, "Ft") {
				build = i
			}
		case *ast.IfStmt:
			if mentions(s.Cond, "circuit") && mentions(s.Body, "PMaxImp") {
				limit = i
			}
		case *ast.ExprStmt:
			if callsMethod(s, "applyLmOptimizerInputs") {
				hook = i
			}
		case *ast.RangeStmt:
			if mentions(s.Body, "Batteries") && mentions(s.Body, "append") {
				move = i
			}
		}
	}

	require.GreaterOrEqual(t, build, 0, "the request literal with Ft is no statement of the function level")
	require.GreaterOrEqual(t, limit, 0, "the circuit's PMaxImp is no statement of the function level")
	require.GreaterOrEqual(t, hook, 0, "applyLmOptimizerInputs is no statement of the function level")
	require.GreaterOrEqual(t, move, 0, "the loop moving the batteries into the request is no statement of the function level")

	assert.Greater(t, hook, build, "applyLmOptimizerInputs has to follow the request literal")
	assert.Greater(t, hook, limit, "applyLmOptimizerInputs has to follow the circuit's PMaxImp")
	assert.Less(t, hook, move, "applyLmOptimizerInputs has to precede the loop moving the batteries")
}

// forkHookCounts lists the names a function has to refer to at least that often:
// a hook placed at two lines of one function is no longer there in full when only
// one of them is left, while the name check of forkHooks stays green.
var forkHookCounts = []struct {
	file, fn, name string
	min            int
}{
	{"core/loadpoint.go", "Loadpoint.fastChargingPhases", "effectiveMinCurrentFor", 2},
	{"core/loadpoint.go", "Loadpoint.fastChargingPhases", "circuitAllowsPhases", 2},
	{"core/loadpoint.go", "Loadpoint.boostPower", "effectiveMinCurrentFor", 2},
}

// referencedCount returns how often a function's body refers to the names
func referencedCount(t *testing.T, file, fn string) map[string]int {
	t.Helper()

	res := make(map[string]int)
	ast.Inspect(funcBody(t, file, fn), func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			res[id.Name]++
		}
		return true
	})

	return res
}

// TestForkHookCounts fails when one of the lines of a hook placed more than once
// in a function was lost
func TestForkHookCounts(t *testing.T) {
	for _, h := range forkHookCounts {
		t.Run(h.file+" "+h.fn+" "+h.name, func(t *testing.T) {
			assert.GreaterOrEqual(t, referencedCount(t, h.file, h.fn)[h.name], h.min, "%s refers to %s too rarely", h.fn, h.name)
		})
	}
}

// touchPointFiles returns the files of the first column of the table "Upstream touch
// points" in core/lm/README.md. A file named without a directory ("Mode.vue") stands
// in the directory of the one before it.
func touchPointFiles(t *testing.T) []string {
	t.Helper()

	b, err := os.ReadFile("lm/README.md")
	require.NoError(t, err)

	text := string(b)
	_, table, ok := strings.Cut(text, "\n## Upstream touch points\n")
	require.True(t, ok, "no section Upstream touch points")
	table, _, ok = strings.Cut(table, "\n## Own files\n")
	require.True(t, ok, "no section Own files after the table")

	quoted := regexp.MustCompile("`([^`]+)`")

	var res []string
	for _, line := range strings.Split(table, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}

		cell := strings.SplitN(line, "|", 3)[1]

		dir := ""
		for _, m := range quoted.FindAllStringSubmatch(cell, -1) {
			name := m[1]
			if strings.Contains(name, "/") {
				dir = path.Dir(name)
			} else {
				name = path.Join(dir, name)
			}
			res = append(res, name)
		}
	}

	require.NotEmpty(t, res)
	return res
}

// touchPointNoMarker lists the files of the table that cannot carry a marker comment
var touchPointNoMarker = map[string]bool{
	"i18n/de.json": true, // json has no comments, the texts are added only
	"i18n/en.json": true,
}

// hasMarker reports whether a file has a "custom" marker in a comment
var markerRegexp = regexp.MustCompile(`(?i)(//|#|/\*|<!--)[^\n]*custom`)

// TestTouchPointTable fails when the table of the touch points in evcc's files and
// the files differ: a file of the table that is gone (renamed in a merge), or one
// without any marker. The other way round, an evcc file changed but not in the
// table, is checked monthly by the skill wartung against the evcc tag: the
// checkout of the tests is shallow.
func TestTouchPointTable(t *testing.T) {
	for _, file := range touchPointFiles(t) {
		t.Run(file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(file)))
			require.NoError(t, err, "%s is in the table of the touch points, but not a file", file)

			if !touchPointNoMarker[file] {
				assert.Regexp(t, markerRegexp, string(b), "%s is in the table of the touch points, but has no custom marker", file)
			}
		})
	}
}
