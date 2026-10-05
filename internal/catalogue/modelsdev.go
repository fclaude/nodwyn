package catalogue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// Source is where the snapshot comes from.
const Source = "https://models.dev/api.json"

// FromModelsDev reads models.dev's api.json into the snapshot: the models of
// each of Providers that write text, with their context and reply limits,
// their reasoning efforts (models.dev's "effort" options; a toggle or a token
// budget is not an effort a request names), whether they read images, and
// their prices, a context tier included. Anything else models.dev says,
// addresses included, is left out.
func FromModelsDev(raw []byte, fetched string) (Data, error) {
	type rates struct {
		Input      *float64 `json:"input"`
		Output     *float64 `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
	}
	type model struct {
		Limit struct {
			Context int `json:"context"`
			Output  int `json:"output"`
		} `json:"limit"`
		ReasoningOptions []struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		} `json:"reasoning_options"`
		Modalities struct {
			Input  []string `json:"input"`
			Output []string `json:"output"`
		} `json:"modalities"`
		Cost *struct {
			rates
			Tiers []struct {
				rates
				Tier struct {
					Type string `json:"type"`
					Size int    `json:"size"`
				} `json:"tier"`
			} `json:"tiers"`
		} `json:"cost"`
	}
	var all map[string]struct {
		Models map[string]model `json:"models"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		return Data{}, fmt.Errorf("catalogue: models.dev does not read: %w", err)
	}
	d := Data{Source: Source, Fetched: fetched, Providers: map[string]map[string]Model{}}
	for _, p := range Providers {
		models := map[string]Model{}
		for id, m := range all[p.ID].Models {
			if !slices.Contains(m.Modalities.Output, "text") {
				continue
			}
			out := Model{Context: m.Limit.Context, Output: m.Limit.Output, Images: slices.Contains(m.Modalities.Input, "image")}
			for _, o := range m.ReasoningOptions {
				if o.Type == "effort" && len(o.Values) > 0 {
					out.Efforts = o.Values
				}
			}
			if c := m.Cost; c != nil && c.Input != nil && c.Output != nil {
				out.Price = &Price{Input: *c.Input, Output: *c.Output, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite}
				for _, t := range c.Tiers {
					if t.Tier.Type == "context" && t.Tier.Size > 0 && t.Input != nil && t.Output != nil {
						out.Price.Tiers = append(out.Price.Tiers, Tier{Above: t.Tier.Size, Input: *t.Input, Output: *t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite})
					}
				}
			}
			models[id] = out
		}
		if len(models) > 0 {
			d.Providers[p.ID] = models
		}
	}
	return d, nil
}

// Encode writes the snapshot as models.json has it: one model to a line, in
// order, so a regeneration's diff shows what each provider changed.
func Encode(d Data) ([]byte, error) {
	var b bytes.Buffer
	head, err := json.Marshal(struct {
		Source  string `json:"source"`
		Fetched string `json:"fetched"`
	}{d.Source, d.Fetched})
	if err != nil {
		return nil, err
	}
	b.Write(head[:len(head)-1])
	b.WriteString(`,"providers":{`)
	for i, p := range slices.Sorted(maps.Keys(d.Providers)) {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "\n%q:{", p)
		for j, id := range slices.Sorted(maps.Keys(d.Providers[p])) {
			line, err := json.Marshal(d.Providers[p][id])
			if err != nil {
				return nil, err
			}
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "\n%q:%s", id, line)
		}
		b.WriteString("}")
	}
	b.WriteString("}}\n")
	return b.Bytes(), nil
}
