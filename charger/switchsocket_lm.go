package charger

// Custom extension: the power a switch device draws when on, entered by the
// user. Load management checks it before switching the device on, and uses it
// while no measurement is available. Without a power sensor it also becomes the
// device's reported power while on.

// RatedPower returns the configured power drawn when on in W, 0 = not set
func (c *SwitchSocket) RatedPower() float64 {
	return c.ratedPower
}
