package dbmap

import (
	"math"
	"sort"
)

// Card metrics shared with the UI (web/dbmap.js: CARD).
const (
	CardW      = 264.0
	CardHead   = 42.0
	CardRow    = 24.0
	CardFoot   = 10.0
	CardGap    = 44.0
	BlockGap   = 120.0
	GroupPad   = 36.0
	CollapsedN = 8 // rows shown of a collapsed card at most (keys first)
)

// CardHeight estimates the height of a table card on the map.
func CardHeight(t *Table, m *TableMeta, keyCols int) float64 {
	n := len(t.Columns)
	if m != nil && m.Collapsed {
		k := keyCols
		if k > CollapsedN {
			k = CollapsedN
		}
		n = k + 1 // "+N további mező"
	}
	if n < 1 {
		n = 1
	}
	return CardHead + float64(n)*CardRow + CardFoot
}

// Layout places tables on the map: every group forms a block (tables of one
// group stay together, related tables next to each other), blocks are
// arranged in rows. With onlyUnplaced only tables without a position are
// placed, below everything already on the map.
func (d *Doc) Layout(sch *Schema, onlyUnplaced bool) {
	if sch == nil {
		return
	}
	d.fix()
	keyCols := d.keyColumns(sch)
	var todo []*Table
	maxY := 0.0
	anyPlaced := false
	for i := range sch.Tables {
		t := &sch.Tables[i]
		m := d.Tables[Norm(t.Key())]
		if m == nil {
			continue
		}
		if m.Placed && onlyUnplaced {
			anyPlaced = true
			if y := m.Y + CardHeight(t, m, keyCols[Norm(t.Key())]); y > maxY {
				maxY = y
			}
			continue
		}
		todo = append(todo, t)
	}
	for _, n := range d.Notes {
		if n.Y+n.H > maxY {
			maxY = n.Y + n.H
		}
	}
	if len(todo) == 0 {
		return
	}
	defer func() {
		if !onlyUnplaced {
			d.placeNotes(sch, keyCols)
		}
	}()
	// blocks by group (in group order), ungrouped last
	order := map[string]int{"": len(d.Groups)}
	for i, g := range d.Groups {
		order[g.ID] = i
	}
	blocks := map[string][]*Table{}
	for _, t := range todo {
		g := d.Tables[Norm(t.Key())].Group
		if _, ok := order[g]; !ok {
			g = ""
		}
		blocks[g] = append(blocks[g], t)
	}
	var gids []string
	for g := range blocks {
		gids = append(gids, g)
	}
	sort.Slice(gids, func(i, j int) bool { return order[gids[i]] < order[gids[j]] })

	adj := d.adjacency(sch)
	x0, y0 := 0.0, 0.0
	if anyPlaced {
		y0 = maxY + BlockGap
	}
	rowH := 0.0
	const maxRowW = 2600.0
	for _, g := range gids {
		ts := connectedOrder(blocks[g], adj)
		cols := int(math.Ceil(math.Sqrt(float64(len(ts)) / 1.4)))
		cols = max(1, min(cols, 6))
		colY := make([]float64, cols)
		pad := 0.0
		if g != "" {
			pad = GroupPad
		}
		for _, t := range ts {
			c := 0
			for i := range colY {
				if colY[i] < colY[c] {
					c = i
				}
			}
			m := d.Tables[Norm(t.Key())]
			m.X = x0 + pad + float64(c)*(CardW+CardGap)
			m.Y = y0 + pad + colY[c]
			m.Placed = true
			colY[c] += CardHeight(t, m, keyCols[Norm(t.Key())]) + CardGap
		}
		w := float64(cols)*(CardW+CardGap) - CardGap + 2*pad
		hgt := 0.0
		for _, v := range colY {
			hgt = math.Max(hgt, v)
		}
		hgt += 2*pad - CardGap
		rowH = math.Max(rowH, hgt)
		x0 += w + BlockGap
		if x0 > maxRowW {
			x0, y0, rowH = 0, y0+rowH+BlockGap, 0
		}
	}
}

// keyColumns counts the columns a collapsed card shows: keys and related.
func (d *Doc) keyColumns(sch *Schema) map[string]int {
	rel := map[string]bool{}
	mark := func(t, c string) { rel[Norm(t)+"|"+Norm(c)] = true }
	for _, r := range d.Relations {
		mark(r.From, r.FromCol)
		mark(r.To, r.ToCol)
	}
	for _, fk := range sch.FKs {
		for _, c := range fk.FromCols {
			mark(fk.From, c)
		}
		for _, c := range fk.ToCols {
			mark(fk.To, c)
		}
	}
	out := map[string]int{}
	for i := range sch.Tables {
		t := &sch.Tables[i]
		n := 0
		for _, c := range t.Columns {
			if c.PK || rel[Norm(t.Key())+"|"+Norm(c.Name)] {
				n++
			}
		}
		out[Norm(t.Key())] = n
	}
	return out
}

func (d *Doc) adjacency(sch *Schema) map[string][]string {
	adj := map[string][]string{}
	link := func(a, b string) {
		a, b = Norm(a), Norm(b)
		if a != b {
			adj[a] = append(adj[a], b)
			adj[b] = append(adj[b], a)
		}
	}
	for _, r := range d.Relations {
		link(r.From, r.To)
	}
	for _, fk := range sch.FKs {
		link(fk.From, fk.To)
	}
	return adj
}

// connectedOrder: the most connected table first, then its neighbours
// (breadth first), so related tables end up near each other.
func connectedOrder(ts []*Table, adj map[string][]string) []*Table {
	byKey := map[string]*Table{}
	for _, t := range ts {
		byKey[Norm(t.Key())] = t
	}
	sorted := append([]*Table(nil), ts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := len(adj[Norm(sorted[i].Key())]), len(adj[Norm(sorted[j].Key())])
		if a != b {
			return a > b
		}
		return Norm(sorted[i].Key()) < Norm(sorted[j].Key())
	})
	seen := map[string]bool{}
	var out []*Table
	for _, start := range sorted {
		k := Norm(start.Key())
		if seen[k] {
			continue
		}
		queue := []string{k}
		seen[k] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			out = append(out, byKey[cur])
			nb := append([]string(nil), adj[cur]...)
			sort.Strings(nb)
			for _, n := range nb {
				if !seen[n] && byKey[n] != nil {
					seen[n] = true
					queue = append(queue, n)
				}
			}
		}
	}
	return out
}

// placeNotes puts the notes in a row below the tables after a full layout,
// so no note covers a card.
func (d *Doc) placeNotes(sch *Schema, keyCols map[string]int) {
	maxY, x := 0.0, 0.0
	for i := range sch.Tables {
		t := &sch.Tables[i]
		if m := d.Tables[Norm(t.Key())]; m != nil {
			maxY = math.Max(maxY, m.Y+CardHeight(t, m, keyCols[Norm(t.Key())]))
		}
	}
	for i := range d.Notes {
		d.Notes[i].X, d.Notes[i].Y = x, maxY+BlockGap
		x += d.Notes[i].W + CardGap
	}
}
