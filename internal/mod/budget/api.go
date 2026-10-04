package budget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// BoxSettings is the box-wide budget settings plus what they are measured
// against.
type BoxSettings struct {
	Settings
	MemoryMB     int     `json:"memoryMB" doc:"The box's memory in MiB (0 off-box)"`
	CPUs         int     `json:"cpus" doc:"The box's CPUs (0 off-box)"`
	ReserveMB    int     `json:"reserveMB" doc:"Memory Tiffin keeps for its own services (API, auth, storage, observability, Postgres's shared buffers, Valkey's cache)"`
	AppMemoryMB  int     `json:"appMemoryMB" doc:"Memory the box keeps for apps, shared by every project: memoryMB minus reserveMB. maxSharePercent and the default share are percentages of this."`
	BudgetedMB   int     `json:"budgetedMB" doc:"Sum of the memoryMB budgets projects set (they must fit in appMemoryMB)"`
	DefaultCPUs  float64 `json:"defaultCpus,omitempty" doc:"The CPU cap the default share gives a project, in cores (absent at 100%)"`
	DefaultMemMB int     `json:"defaultMemoryMB,omitempty" doc:"The memory cap the default share gives a project, in MiB (absent at 100%)"`
	Explanation  string  `json:"explanation" doc:"How the memory for apps is computed, in plain words"`
}

type settingsBody struct {
	DefaultMaxSharePercent int `json:"defaultMaxSharePercent" minimum:"5" maximum:"100" doc:"Projects that set no limit of their own in tiffin.config.ts may use at most this percentage of the box (of the memory it keeps for apps and of its CPUs). 100 means elastic: no cap beyond what keeps the platform and other projects safe."`
}

// RegisterAPI adds the box budget settings.
func (m *Module) RegisterAPI(a huma.API, _ *platform.Platform) {
	huma.Register(a, api.Op("box-settings-get", http.MethodGet, "/v1/box/settings", "box settings get", api.RiskRead,
		"Show how the box is shared",
		"The box-wide default share (defaultMaxSharePercent: projects that set no `resources` of their own may use at most this "+
			"percentage of the box; 100 = elastic), the box's memory and CPUs, the memory Tiffin keeps for itself, the memory left for "+
			"apps and how much of it projects' memoryMB budgets take. Per-project limits and live use: projects usage.", "system"),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body BoxSettings }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			return &struct{ Body BoxSettings }{m.boxSettings(ctx)}, nil
		}))

	huma.Register(a, api.Op("box-settings-set", http.MethodPut, "/v1/box/settings", "box settings set", api.RiskWrite,
		"Set the default share of the box",
		"Sets defaultMaxSharePercent: every project that sets no `resources` in tiffin.config.ts may use at most this percentage of "+
			"the box, of its memory for apps and of its CPUs (e.g. 25 = no project can take more than a quarter unless its config says "+
			"otherwise). 100 restores elastic sharing. Applies live to running apps without restarting them; a project over its new cap "+
			"has its cache reclaimed, and an app that cannot fit is killed for memory and restarted. Box owner only.", "system"),
		api.Wrap(func(ctx context.Context, in *struct{ Body settingsBody }) (*struct{ Body BoxSettings }, error) {
			if !api.PrincipalFrom(ctx).BoxAdmin() {
				return nil, fmt.Errorf("%w: changing how the box is shared needs the box owner's token", tokens.ErrForbidden)
			}
			pct := in.Body.DefaultMaxSharePercent
			if pct < 5 || pct > 100 {
				return nil, api.NewProblem(422, "validation", "defaultMaxSharePercent must be between 5 and 100")
			}
			m.mu.Lock()
			p := m.p
			m.mu.Unlock()
			if p == nil {
				return nil, api.NewProblem(503, "internal", "box settings live on a box; this server runs without --box")
			}
			raw, _ := json.Marshal(Settings{DefaultMaxSharePercent: pct})
			if err := p.DB.KVPut(ctx, kvNS, kvSettings, raw); err != nil {
				return nil, err
			}
			m.mu.Lock()
			m.settings.DefaultMaxSharePercent = pct
			m.mu.Unlock()
			if err := m.sync(ctx); err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, api.PrincipalFrom(ctx).TokenID, "box.settings_set", "", map[string]any{"defaultMaxSharePercent": pct})
			return &struct{ Body BoxSettings }{m.boxSettings(ctx)}, nil
		}))
}

func (m *Module) boxSettings(context.Context) BoxSettings {
	box, ok := ReadBox(m.root)
	m.mu.Lock()
	set := m.settings
	if m.specs == nil {
		set = DefaultSettings
	}
	budgeted := 0
	for _, r := range m.specs {
		if r != nil {
			budgeted += r.MemoryMB
		}
	}
	m.mu.Unlock()
	out := BoxSettings{Settings: set, BudgetedMB: budgeted}
	if !ok {
		out.Explanation = "This server is not a Linux box, so there is nothing to divide."
		return out
	}
	out.MemoryMB, out.CPUs = box.MemoryMB, box.CPUs
	out.ReserveMB, out.AppMemoryMB = ReserveFor(box.MemoryMB).TotalMB, box.PoolMB()
	if set.DefaultMaxSharePercent < 100 {
		l := Resolve(box, set, []Project{{Name: "x"}})["x"]
		out.DefaultCPUs, out.DefaultMemMB = l.CPUs, l.MemoryMaxMB
	}
	out.Explanation = box.Explain()
	return out
}
