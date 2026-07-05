package loc

// Location represents a named point in a Quake map
type Location struct {
	X, Y, Z float32 // World coordinates (loc file coords divided by 8)
	Name    string  // Human-readable location name (variables substituted)
}

// Finder provides nearest-location lookup for a map. FindNearest is
// backed by a lazily-built XY-pencil cell index (see index.go) — beats
// linear scan once L grows past a few hundred; competitive-map sizes
// (L ≤ ~316) wouldn't care either way.
type Finder struct {
	mapName   string
	locations []Location
	index     indexOnce
}

// MapName returns the name of the map this finder is for
func (f *Finder) MapName() string {
	return f.mapName
}

// NewFinder constructs a Finder from an explicit name and location list.
// Intended for tests and for tooling that has already parsed its own
// loc data; normal callers should use LoadForMap.
func NewFinder(mapName string, locations []Location) *Finder {
	return &Finder{mapName: mapName, locations: locations}
}

// LocationCount returns the number of locations loaded
func (f *Finder) LocationCount() int {
	return len(f.locations)
}
