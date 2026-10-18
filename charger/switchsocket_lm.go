package charger

// Custom extension: the power a switch device draws when on, entered by the
// user. Load management checks it before switching the device on, and uses it
// while no measurement is available. Without a power sensor it also becomes the
// device's reported power while on.

// RatedPower returns the power drawn when on in W, 0 = not set: the configured
// ratedpower, else the negative standbypower (its amount), which is how a switch
// without a power sensor states what it draws
func (c *SwitchSocket) RatedPower() float64 {
	if c.ratedPower > 0 {
		return c.ratedPower
	}
	if c.standbypower < 0 {
		return -c.standbypower
	}
	return 0
}
