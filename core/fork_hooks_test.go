package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
	{"core/loadpoint.go", "Loadpoint.pvScalePhases", []string{"lmm", "PeekCurrent", "min1pCurrentOr", "effectiveMinCurrentFor", "phaseScaleDelay"}},
	{"core/loadpoint.go", "Loadpoint.boostPower", []string{"effectiveMinCurrentFor"}},
	{"core/loadpoint.go", "Loadpoint.scalePhases", []string{"minCurrentScaleUp"}},
	{"core/loadpoint.go", "Loadpoint.pvMaxCurrent", []string{"effectiveMinCurrentFor", "effectiveMaxCurrentFor", "projectPhaseSwitch1p", "holdStage"}},
	{"core/loadpoint_effective.go", "Loadpoint.effectiveMinCurrent", []string{"effectiveMinCurrentFor", "currents1pPhases"}},
	{"core/loadpoint_effective.go", "Loadpoint.effectiveMaxCurrent", []string{"effectiveMaxCurrentFor", "currents1pPhases"}},
	{"core/loadpoint/config.go", "DynamicConfig.Apply", []string{"applyPhaseSwitch"}},
	{"core/circuit/circuit.go", "Circuit.Update", []string{"overPowerLog"}},
	{"server/http.go", "HTTPd.RegisterSiteHandlers", []string{"addCustomSiteRoutes"}},
	{"server/http_config_loadpoint_handler.go", "getLoadpointDynamicConfig", []string{"PhaseSwitchConfigOf"}},
	{"server/http_config_helper.go", "testInstance", []string{"customTestResults"}},
	{"server/http_config_device_handler.go", "cleanupTariffRef", []string{"FeedInEeg"}},
	{"cmd/setup.go", "configureTariffs", []string{"FeedInEeg"}},
	{"api/globalconfig/types.go", "TariffRefs.IsConfigured", []string{"FeedInEeg"}},
	{"api/globalconfig/types.go", "TariffRefs.Used", []string{"FeedInEeg"}},
	{"charger/switchsocket.go", "NewSwitchSocketFromConfig", []string{"RatedPower"}},
	{"util/log.go", "newLogger", []string{"Output"}},
	{"cmd/root.go", "runRoot", []string{"CloseFile"}},
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
	{"assets/js/views/Battery.vue", []string{"<BatterySocGridChargeCard", "<BatteryPeakShavingCard", "<BatteryProfileCard"}},
	{"assets/js/components/BottomTabs/MoreMenu.vue", []string{"<LmMoreMenuItems"}},
	{"assets/js/components/Config/CircuitModal.vue", []string{"<CircuitPeakHint", "CircuitPeakHint,"}},
	{"assets/js/components/Config/LoadpointModal.vue", []string{"<PhaseSwitchFields", "emptyUnsetPhaseSwitch", "chargerIsStages"}},
	{"assets/js/components/Loadpoints/Loadpoint.vue", []string{"chargerStages: Boolean", "chargePowerEstimate"}},
	{"assets/js/components/Site/Site.vue", []string{"loadpoints: measuredLoadpoints(this.enabledLoadpoints)"}},
	{"assets/js/components/Loadpoints/Mode.vue",[]string{"chargerStages: Boolean", "this.switchDevice || this.chargerStages"}},
	{"assets/js/views/Forecast.vue", []string{"@adjust=\"changeAdjusted\"", "form-switch mb-0 text-nowrap d-none\"", "SnowCoverSwitch, // custom"}},
	{"assets/js/views/Log.vue", []string{"<LogFileButton", "LogFileButton,"}},
	{"assets/js/components/Config/DeviceTags.vue", []string{"case \"stages\": // custom"}},
	{"assets/js/components/Config/TariffCard.vue", []string{"<FeedInEegSummary"}},
	{"assets/js/components/Config/TariffModal.vue", []string{"feedInEeg", "tariff-planner-optimizer"}},
	{"assets/js/views/Energy.vue", []string{"gridChartSeries", ":feed-in-eeg=\"feedInEeg\"", "loadFeedInSplit(requestKey)", "withoutFeedInEeg("}},
	{"assets/js/components/Energy/GroupChart.vue", []string{"s.returnColor ||"}},
	{"assets/js/components/Energy/GridStats.vue", []string{"feedInEegStats(this.feedInEeg)"}},
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
