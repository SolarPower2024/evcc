package tariff

// Custom extension: the OeMAG market price, the feed-in compensation of the
// Austrian settlement agency. OeMAG publishes a month's price only after the
// month has ended. The latest published value is used as the running feed-in
// price, and from the finalize day on it is taken as the final price of the
// previous month, which core/site_feedin.go then applies retroactively.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/request"
	"github.com/jinzhu/now"
)

const oemagURI = "https://raw.githubusercontent.com/chrsbrmr/oemag-marktpreis/refs/heads/main/preis.json"

type Oemag struct {
	*embed
	log         *util.Logger
	uri         string
	finalizeDay int
	data        *util.Monitor[float64]
}

var _ api.Tariff = (*Oemag)(nil)

func init() {
	registry.Add("oemag", NewOemagFromConfig)
}

func NewOemagFromConfig(other map[string]any) (api.Tariff, error) {
	cc := struct {
		embed       `mapstructure:",squash"`
		URI         string
		FinalizeDay int
	}{
		URI:         oemagURI,
		FinalizeDay: 15,
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	if err := cc.init(); err != nil {
		return nil, err
	}

	if cc.FinalizeDay < 1 || cc.FinalizeDay > 28 {
		return nil, fmt.Errorf("finalize day must be between 1 and 28: %d", cc.FinalizeDay)
	}

	t := &Oemag{
		embed:       &cc.embed,
		log:         util.NewLogger("oemag"),
		uri:         cc.URI,
		finalizeDay: cc.FinalizeDay,
		data:        util.NewMonitor[float64](25 * time.Hour),
	}

	return runOrError(t)
}

// oemagPrice is the published document
type oemagPrice struct {
	Price *float64 `json:"oemag_marktpreis"`
	Unit  string   `json:"unit"`
}

func (t *Oemag) run(done chan error) {
	var once sync.Once
	client := request.NewHelper(t.log)

	for tick := time.Tick(3 * time.Hour); ; <-tick {
		price, err := backoff.RetryWithData(func() (float64, error) {
			var res oemagPrice
			if err := client.GetJSON(t.uri, &res); err != nil {
				// a malformed document does not get better by asking again
				var syntax *json.SyntaxError
				var typ *json.UnmarshalTypeError
				if errors.As(err, &syntax) || errors.As(err, &typ) {
					return 0, backoff.Permanent(err)
				}
				return 0, backoffPermanentError(err)
			}
			return validOemagPrice(res)
		}, bo())
		if err != nil {
			if reportError(&once, done, err) {
				return
			}
			t.log.ERROR.Println(err)
			continue
		}

		t.data.Set(price)
		once.Do(func() { close(done) })
	}
}

// validOemagPrice guards against a broken scrape: the source is not official
func validOemagPrice(res oemagPrice) (float64, error) {
	if res.Price == nil {
		return 0, backoff.Permanent(errors.New("missing oemag_marktpreis"))
	}
	if res.Unit != "" && res.Unit != "EUR/kWh" {
		return 0, backoff.Permanent(fmt.Errorf("unexpected unit: %s", res.Unit))
	}
	if p := *res.Price; p <= 0 || p >= 1 {
		return 0, backoff.Permanent(fmt.Errorf("implausible price: %v EUR/kWh", p))
	}
	return *res.Price, nil
}

// Rates implements the api.Tariff interface: the published price, flat
func (t *Oemag) Rates() (api.Rates, error) {
	var price float64
	if err := t.data.GetFunc(func(v float64) { price = v }); err != nil {
		return nil, err
	}

	var res api.Rates

	start := now.BeginningOfDay()
	for i := range 7 {
		ts := start.AddDate(0, 0, i)
		res = append(res, api.Rate{
			Start: ts,
			End:   ts.AddDate(0, 0, 1),
			Value: t.totalPrice(price, ts),
		})
	}

	return slices.Clip(res), nil
}

// Type implements the api.Tariff interface
func (t *Oemag) Type() api.TariffType {
	return api.TariffTypePriceStatic
}

// FinalizeDay returns the day of the month from which the published price is
// the final price of the previous month
func (t *Oemag) FinalizeDay() int {
	return t.finalizeDay
}

// MarketPrice returns the latest published market price in EUR/kWh, from the
// finalize day on the final price of the previous month
func (t *Oemag) MarketPrice() (float64, error) {
	var price float64
	err := t.data.GetFunc(func(v float64) { price = v })
	return price, err
}

// TotalPrice returns a market price as the feed-in rate at ts, with the
// configured charges and tax applied like for the running rates
func (t *Oemag) TotalPrice(market float64, ts time.Time) float64 {
	return t.totalPrice(market, ts)
}

// ValidMarketPrice checks a market price entered by hand like a published one
func ValidMarketPrice(price float64) error {
	_, err := validOemagPrice(oemagPrice{Price: &price, Unit: "EUR/kWh"})
	return err
}
