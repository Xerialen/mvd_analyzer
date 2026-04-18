package analyzer

import (
	"encoding/json"
	"sort"

	"github.com/mvd-analyzer/qwdemo/events"
)

// cleanQuakeName normalizes a Quake-encoded name from KTX's demoinfo JSON.
// JSON escapes like \u00CE come back as rune 0xCE, so we treat each rune as
// a Quake font byte (0-255) and run it through the same Q_normalizetext
// table that the parser uses on userinfo / print messages. Keeping a single
// mapping function in two places is fragile, but the parser package owns
// the canonical table; analyzer just delegates.
func cleanQuakeName(s string) string {
	if s == "" {
		return ""
	}
	buf := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 0 || r > 255 {
			// Non-byte runes shouldn't appear in Quake names; drop them
			// rather than mangle to something arbitrary.
			continue
		}
		buf = append(buf, byte(r))
	}
	return events.NormalizeQuakeText(buf)
}

// DemoInfoAnalyzer collects and parses embedded demoinfo JSON from hidden messages
type DemoInfoAnalyzer struct {
	ctx    *Context
	blocks map[int][]byte // blockNum -> content
}

// NewDemoInfoAnalyzer creates a new demoinfo analyzer
func NewDemoInfoAnalyzer() *DemoInfoAnalyzer {
	return &DemoInfoAnalyzer{
		blocks: make(map[int][]byte),
	}
}

func (a *DemoInfoAnalyzer) Name() string { return "demoinfo" }

func (a *DemoInfoAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

func (a *DemoInfoAnalyzer) OnEvent(event events.Event) error {
	if e, ok := event.(*events.DemoInfoEvent); ok {
		a.blocks[e.BlockNum] = e.Content
	}
	return nil
}

func (a *DemoInfoAnalyzer) Finalize() (interface{}, error) {
	if len(a.blocks) == 0 {
		return nil, nil
	}

	result := a.parseBlocks()

	// Store in context for other analyzers
	if result != nil {
		a.ctx.DemoInfo = result
	}

	return result, nil
}

func (a *DemoInfoAnalyzer) parseBlocks() *DemoInfoResult {

	// Concatenate blocks in correct order
	// Block numbering: 1, 2, 3, ..., 0 (where 0 is the LAST block)
	var blockNums []int
	for num := range a.blocks {
		blockNums = append(blockNums, num)
	}
	sort.Ints(blockNums)

	// Reorder: move block 0 to the end if present
	if len(blockNums) > 0 && blockNums[0] == 0 {
		blockNums = append(blockNums[1:], 0)
	}

	var fullJSON []byte
	for _, num := range blockNums {
		fullJSON = append(fullJSON, a.blocks[num]...)
	}

	// Parse the JSON
	var raw DemoInfoRaw
	if err := json.Unmarshal(fullJSON, &raw); err != nil {
		// Return raw JSON as string for debugging if parsing fails
		return &DemoInfoResult{
			RawJSON: string(fullJSON),
		}
	}

	// Convert to our result structure
	// Clean team names (remove Quake high-bit color codes)
	cleanedTeams := make([]string, len(raw.Teams))
	for i, t := range raw.Teams {
		cleanedTeams[i] = cleanQuakeName(t)
	}

	result := &DemoInfoResult{
		Version:   raw.Version,
		Date:      raw.Date,
		Map:       raw.Map,
		Hostname:  raw.Hostname,
		IP:        raw.IP,
		Port:      raw.Port,
		Mode:      raw.Mode,
		Timelimit: raw.Timelimit,
		Fraglimit: raw.Fraglimit,
		Duration:  raw.Duration,
		Demo:      raw.Demo,
		Teams:     cleanedTeams,
		Players:   make([]DemoInfoPlayer, 0, len(raw.Players)),
	}

	for _, p := range raw.Players {
		player := DemoInfoPlayer{
			Name:        cleanQuakeName(p.Name),
			Team:        cleanQuakeName(p.Team),
			TopColor:    p.TopColor,
			BottomColor: p.BottomColor,
			Ping:        p.Ping,
			Login:       p.Login,
			Handicap:    p.Handicap,
			Bot:         p.Bot,
			Stats:       p.Stats,
			Dmg:         p.Dmg,
			Spree:       p.Spree,
			Control:     p.Control,
			Speed:       p.Speed,
			XferRL:      p.XferRL,
			XferLG:      p.XferLG,
			Weapons:     make(map[string]*DemoInfoWeapon),
			Items:       make(map[string]*DemoInfoItem),
		}

		for name, w := range p.Weapons {
			player.Weapons[name] = &DemoInfoWeapon{
				Acc:     w.Acc,
				Kills:   w.Kills,
				Deaths:  w.Deaths,
				Pickups: w.Pickups,
				Damage:  w.Damage,
			}
		}

		for name, it := range p.Items {
			player.Items[name] = &DemoInfoItem{
				Took: it.Took,
				Time: it.Time,
			}
		}

		result.Players = append(result.Players, player)
	}

	return result
}

// Raw JSON structures for parsing KTX demoinfo

// DemoInfoRaw is the raw JSON structure from KTX
type DemoInfoRaw struct {
	Version   int                    `json:"version"`
	Date      string                 `json:"date"`
	Map       string                 `json:"map"`
	Hostname  string                 `json:"hostname"`
	IP        string                 `json:"ip"`
	Port      int                    `json:"port"`
	Matchtag  string                 `json:"matchtag,omitempty"`
	Mode      string                 `json:"mode,omitempty"`
	Timelimit int                    `json:"tl,omitempty"`
	Fraglimit int                    `json:"fl,omitempty"`
	Deathmatch int                   `json:"dm,omitempty"`
	Teamplay  int                    `json:"tp,omitempty"`
	Duration  int                    `json:"duration"`
	Demo      string                 `json:"demo,omitempty"`
	Teams     []string               `json:"teams,omitempty"`
	Players   []DemoInfoPlayerRaw    `json:"players"`
}

// DemoInfoPlayerRaw is the raw player structure from KTX JSON
type DemoInfoPlayerRaw struct {
	TopColor    int                          `json:"top-color"`
	BottomColor int                          `json:"bottom-color"`
	Ping        int                          `json:"ping"`
	Login       string                       `json:"login"`
	Name        string                       `json:"name"`
	Team        string                       `json:"team"`
	Stats       *DemoInfoStats               `json:"stats,omitempty"`
	Dmg         *DemoInfoDmg                 `json:"dmg,omitempty"`
	XferRL      int                          `json:"xferRL,omitempty"`
	XferLG      int                          `json:"xferLG,omitempty"`
	Spree       *DemoInfoSpree               `json:"spree,omitempty"`
	Control     float64                      `json:"control,omitempty"`
	Speed       *DemoInfoSpeed               `json:"speed,omitempty"`
	Handicap    int                          `json:"handicap,omitempty"`
	Bot         *DemoInfoBot                 `json:"bot,omitempty"`
	Weapons     map[string]*DemoInfoWeaponRaw `json:"weapons,omitempty"`
	Items       map[string]*DemoInfoItemRaw   `json:"items,omitempty"`
}

// DemoInfoWeaponRaw is the raw weapon structure from KTX JSON
type DemoInfoWeaponRaw struct {
	Acc     *DemoInfoAcc     `json:"acc,omitempty"`
	Kills   *DemoInfoKills   `json:"kills,omitempty"`
	Deaths  int              `json:"deaths,omitempty"`
	Pickups *DemoInfoPickups `json:"pickups,omitempty"`
	Damage  *DemoInfoDamage  `json:"damage,omitempty"`
}

// DemoInfoItemRaw is the raw item structure from KTX JSON
type DemoInfoItemRaw struct {
	Took int `json:"took,omitempty"`
	Time int `json:"time,omitempty"`
}
