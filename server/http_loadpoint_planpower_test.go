package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// limitedLoadpoint plans with 6500 W of 11040 W and says why, unless it is not limited
type limitedLoadpoint struct {
	*loadpoint.MockAPI
	limited bool
}

func (lp limitedLoadpoint) PlanPower(power float64) (float64, string) {
	if lp.limited {
		return 6500, "peak"
	}
	return power, ""
}

// planPowerRequest calls a plan route of the loadpoint and returns the answer's json
func planPowerRequest(t *testing.T, lp loadpoint.API, route, url string) map[string]any {
	t.Helper()

	router := mux.NewRouter()
	switch route {
	case "plan":
		router.HandleFunc("/plan", planHandler(lp))
	default:
		router.HandleFunc("/plan/static/preview/{type:[a-z]+}/{value:[0-9.]+}/{time:[0-9TZ:+-]+}", staticPlanPreviewHandler(lp))
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var res map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	return res
}

func planPowerMock(t *testing.T) *loadpoint.MockAPI {
	t.Helper()

	lp := loadpoint.NewMockAPI(gomock.NewController(t))
	lp.EXPECT().EffectiveMaxPower().Return(11040.0).AnyTimes()
	lp.EXPECT().EffectivePlanTime().Return(time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)).AnyTimes()
	lp.EXPECT().EffectivePlanId().Return(1).AnyTimes()
	lp.EXPECT().GetPlanGoal().Return(40.0, false).AnyTimes()
	lp.EXPECT().GetPlanRequiredDuration(gomock.Any(), gomock.Any()).Return(6 * time.Hour).AnyTimes()
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{}).AnyTimes()
	lp.EXPECT().EffectivePlan(gomock.Any(), gomock.Any(), gomock.Any()).Return(api.Rates{}, 0.0, false).AnyTimes()
	lp.EXPECT().SocBasedPlanning().Return(false).AnyTimes()
	lp.EXPECT().GetPlan(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(api.Rates{}).AnyTimes()
	return lp
}

func TestPlanRoutesPlanPower(t *testing.T) {
	const previewURL = "/plan/static/preview/energy/40/2026-11-17T07:00:00Z"

	for _, route := range []struct{ name, url string }{{"plan", "/plan"}, {"preview", previewURL}} {
		t.Run(route.name+" limited", func(t *testing.T) {
			res := planPowerRequest(t, limitedLoadpoint{planPowerMock(t), true}, route.name, route.url)
			assert.Equal(t, 6500.0, res["power"])
			assert.Equal(t, "peak", res["powerReason"])
		})

		t.Run(route.name+" without a limit", func(t *testing.T) {
			res := planPowerRequest(t, limitedLoadpoint{planPowerMock(t), false}, route.name, route.url)
			assert.Equal(t, 11040.0, res["power"])
			assert.NotContains(t, res, "powerReason")
		})

		t.Run(route.name+" loadpoint without plan power", func(t *testing.T) {
			res := planPowerRequest(t, planPowerMock(t), route.name, route.url)
			assert.Equal(t, 11040.0, res["power"])
			assert.NotContains(t, res, "powerReason")
		})
	}
}

// TestPlanRouteOptimizerHasNoReason: the optimizer's own plan carries its own power
func TestPlanRouteOptimizerHasNoReason(t *testing.T) {
	lp := loadpoint.NewMockAPI(gomock.NewController(t))
	lp.EXPECT().EffectiveMaxPower().Return(11040.0).AnyTimes()
	lp.EXPECT().EffectivePlanTime().Return(time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)).AnyTimes()
	lp.EXPECT().EffectivePlanId().Return(1).AnyTimes()
	lp.EXPECT().GetPlanGoal().Return(40.0, false).AnyTimes()
	lp.EXPECT().GetPlanRequiredDuration(gomock.Any(), gomock.Any()).Return(6 * time.Hour).AnyTimes()
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{}).AnyTimes()
	lp.EXPECT().EffectivePlan(gomock.Any(), gomock.Any(), gomock.Any()).Return(api.Rates{}, 3000.0, true).AnyTimes()

	res := planPowerRequest(t, limitedLoadpoint{lp, true}, "plan", "/plan")
	assert.Equal(t, 3000.0, res["power"])
	assert.Equal(t, true, res["optimizer"])
	assert.NotContains(t, res, "powerReason")
}
