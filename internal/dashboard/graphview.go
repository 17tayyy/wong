package dashboard

import (
	"cmp"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// graphNode is a note as the graph view draws it.
type graphNode struct {
	Path  string   `json:"p"`
	Name  string   `json:"n"`
	Shelf string   `json:"s"`
	Kind  string   `json:"k,omitempty"`
	Tags  []string `json:"t,omitempty"`
	State string   `json:"st,omitempty"`
}

// graphData is the notes kept for a view and the links between them, as pairs of node indexes.
type graphData struct {
	Nodes []graphNode `json:"nodes"`
	Edges [][2]int    `json:"edges"`
}

// graphOf joins the notes keep lets through by the links that resolve between them.
func graphOf(b *brain, keep func(*entry) bool) graphData {
	d := graphData{Nodes: []graphNode{}, Edges: [][2]int{}}
	index := map[string]int{}
	for _, s := range b.Shelves {
		for _, listed := range s.Entries {
			e := b.byPath[listed.Path]
			if !keep(e) {
				continue
			}
			state := ""
			if len(e.Issues) > 0 {
				state = "review"
				if e.Stale() {
					state = "stale"
				}
			}
			index[e.Path] = len(d.Nodes)
			d.Nodes = append(d.Nodes, graphNode{e.Path, e.Name, s.ID, string(e.Kind), e.Note.Tags, state})
		}
	}
	seen := map[[2]int]bool{}
	for _, path := range slices.Sorted(maps.Keys(index)) {
		i := index[path]
		for _, l := range b.byPath[path].Links {
			j, ok := index[l]
			edge := [2]int{min(i, j), max(i, j)}
			if !ok || i == j || seen[edge] {
				continue
			}
			seen[edge] = true
			d.Edges = append(d.Edges, edge)
		}
	}
	slices.SortFunc(d.Edges, func(a, b [2]int) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) })
	return d
}

func (g graphData) json() string {
	data, _ := json.Marshal(g)
	return string(data)
}

// local is the graph around one note: the note, the notes it links to and the notes that link to it.
func local(b *brain, e *entry) graphData {
	around := map[string]bool{e.Path: true}
	for _, l := range e.Links {
		around[l] = true
	}
	for _, l := range e.Backlinks {
		around[l] = true
	}
	return graphOf(b, func(o *entry) bool { return around[o.Path] })
}

func (d *Dashboard) graph(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "graph", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "Graph"
	data := graphOf(b, func(*entry) bool { return true })
	p.Graph, p.GraphNotes, p.GraphLinks = data.json(), len(data.Nodes), len(data.Edges)
	tags, kinds := map[string]bool{}, map[string]bool{}
	for _, n := range data.Nodes {
		for _, t := range n.Tags {
			tags[strings.ToLower(t)] = true
		}
		if n.Kind != "" {
			kinds[n.Kind] = true
		}
	}
	p.TagNames, p.KindNames = slices.Sorted(maps.Keys(tags)), slices.Sorted(maps.Keys(kinds))
	for _, s := range b.Shelves {
		p.Folders = append(p.Folders, s.ID)
	}
	d.render(w, "graph", p)
}
