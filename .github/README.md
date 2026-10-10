# evcc with load management, battery and peak shaving

This is a fork of **evcc**, the open source EV charge controller and home
energy management system. Everything evcc does works here unchanged; the fork
only adds the features below. Unofficial, not part of the evcc project.

The original evcc description is in [README.md](../README.md).

## Additional features

- Load management priorities
- Battery in load management
- Switch devices
- Heater in stages
- Shed guard
- Loads not following their limit
- Load management circuit and switch
- Load management overview
- Phase switching: 1p currents and delays
- Battery grid charging by soc and one-time
- Peak shaving, also for a Marstek battery behind Omnibattery
- Battery profiles
- Home consumption forecast
- Battery identification
- Second feed-in tariff (EEG)
- Optimizer inputs and export forecast
- Advanced settings
- Log file
- Snow on PV

Everything is set up in the evcc ui, nothing in `evcc.yaml`. Without
configuration the fork behaves exactly like evcc.

Install as Home Assistant add-on: [SolarPower2024/evcc-addon](https://github.com/SolarPower2024/evcc-addon)

## Details

Descriptions and settings of all features: [core/lm/README.md](../core/lm/README.md)
