package intel

func MockPins() {
	pins, err := loadPins("./testdata/dpll-pins.json")
	if err != nil {
		panic(err)
	}
	// Mock DPLL pins
	for _, pin := range *pins {
		DpllPins = append(DpllPins, &pin)
	}
}
