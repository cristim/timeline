package model

const PropertyDatatypeNumber = "number"

type UnitConversion struct {
	Scale  float64 `json:"scale"`
	Offset float64 `json:"offset,omitempty"`
}

type PropertyDef struct {
	Label         string                    `json:"label"`
	Datatype      string                    `json:"datatype"`
	CanonicalUnit string                    `json:"canonical_unit"`
	Conversions   map[string]UnitConversion `json:"conversions"`
}

// PropertyDefinitions is the closed DM-6 vocabulary emitted in the manifest.
// The map key is the property id used by claims.
var PropertyDefinitions = map[string]PropertyDef{
	"age":              numberProperty("Age", "a"),
	"area":             numberProperty("Area", "km2"),
	"casualties_total": numberProperty("Total casualties", "count"),
	"deaths":           numberProperty("Deaths", "count"),
	"deaths_europe":    numberProperty("Deaths in Europe", "count"),
	"height":           numberProperty("Height", "m"),
	"length":           numberProperty("Length", "m"),
	"mass":             numberProperty("Mass", "kg"),
	"max_speed":        numberProperty("Maximum speed", "km/h"),
	"population":       numberProperty("Population", "count"),
	"transistor_count": numberProperty("Transistor count", "count"),
	"troops_france":    numberProperty("French troops", "count"),
	"units_produced":   numberProperty("Units produced", "count"),
	"warming":          numberProperty("Warming", "Cel"),
}

func numberProperty(label, unit string) PropertyDef {
	return PropertyDef{
		Label:         label,
		Datatype:      PropertyDatatypeNumber,
		CanonicalUnit: unit,
		Conversions: map[string]UnitConversion{
			unit: {Scale: 1},
		},
	}
}
