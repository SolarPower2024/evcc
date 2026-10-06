package server

// Custom extension: values the fork adds to a device's status on the config
// page, probed by testInstance through one hook.

// stagesReader is implemented by charger.SwitchStages
type stagesReader interface {
	StageStates() ([]bool, error)
}

// customTestResults adds the fork's values: the switch of each stage of a
// heater in stages
func customTestResults(instance any, makeResult func(string, any, error)) {
	if dev, ok := instance.(stagesReader); ok {
		val, err := dev.StageStates()
		makeResult("stages", val, err)
	}
}
