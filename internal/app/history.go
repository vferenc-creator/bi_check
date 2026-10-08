package app

import (
	"errors"
	"log"
	"sort"
	"time"

	"bimonitor/internal/engine"
	"bimonitor/internal/model"
	"bimonitor/internal/notify"
	"bimonitor/internal/schedule"
	"bimonitor/internal/store"
)

// handleEvents records history and feeds the notification policy.
func (a *App) handleEvents(evs []engine.Event) {
	s := a.Settings.Get()
	now := time.Now()
	var notices []notify.Notice
	for _, ev := range evs {
		switch ev.Kind {
		case engine.EvTransition:
			log.Printf("[%s] %s → %s: %s", ev.Item.Name, ev.Old.Status, ev.New.Status, ev.New.Reason)
			if a.History != nil && ev.Old.Status != model.StatusUnknown {
				var exp *time.Time
				if !ev.New.Expected.IsZero() {
					e := ev.New.Expected
					exp = &e
				}
				if err := a.History.RecordTransition(store.Transition{ItemID: ev.Item.ID, At: now, From: string(ev.Old.Status),
					To: string(ev.New.Status), Reason: ev.New.Reason, Expected: exp}); err != nil {
					log.Printf("history: %v", err)
				}
			}
			muted := s.Overrides[ev.Item.ID].Mute || s.GroupMuted(ev.Item.Group)
			notices = append(notices, a.policy.OnTransition(ev, s, muted, now)...)
		case engine.EvArrival:
			if a.History == nil {
				continue
			}
			ar := store.Arrival{ItemID: ev.Item.ID, FilePath: ev.File.Path, ModTime: ev.File.ModTime, Size: ev.File.Size, SeenAt: now}
			if !ev.Expected.IsZero() {
				e := ev.Expected
				d := int64(ev.Delay / time.Second)
				ar.Expected, ar.DelaySec = &e, &d
			}
			if _, err := a.History.RecordArrival(ar); err != nil {
				log.Printf("history: %v", err)
			}
		}
	}
	if len(notices) > 0 {
		a.queueNotices(notices)
	}
}

// queueNotices batches notices for two seconds so a burst of problems
// (e.g. a whole server going down) becomes one notification.
func (a *App) queueNotices(ns []notify.Notice) {
	a.noticeMu.Lock()
	defer a.noticeMu.Unlock()
	a.noticeBuf = append(a.noticeBuf, ns...)
	if a.noticeTimer == nil {
		a.noticeTimer = time.AfterFunc(2*time.Second, a.flushNotices)
	}
}

func (a *App) flushNotices() {
	a.noticeMu.Lock()
	ns := a.noticeBuf
	a.noticeBuf = nil
	a.noticeTimer = nil
	a.noticeMu.Unlock()
	if len(ns) == 0 {
		return
	}
	for _, t := range notify.RenderToasts(ns) {
		kind := NotifyApp
		if t.Error {
			kind = NotifyError
		}
		a.P.Notify(t.Title, t.Text, kind)
	}
}

// background runs periodic housekeeping: quiet-hour summaries, uptime
// heartbeat and pruning.
func (a *App) background(stop <-chan struct{}) {
	var up *store.Uptime
	if a.History != nil {
		up = a.History.StartUptime(time.Now())
	}
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	// First prune ~1 hour after start: by then shared lists are loaded, so
	// their incidents are not mistaken for deleted items.
	lastPrune := time.Now().Add(-5 * time.Hour)
	for {
		select {
		case <-stop:
			up.Beat(time.Now())
			return
		case now := <-t.C:
			up.Beat(now)
			s := a.Settings.Get()
			states := a.Engine.States()
			names := map[string]string{}
			for _, it := range a.effectiveItems(s) {
				names[it.ID] = it.Name
			}
			if ns := a.policy.Tick(s, now.In(a.Loc), states, names); len(ns) > 0 {
				a.queueNotices(ns)
			}
			if a.refreshShared(false) {
				a.reconfigure()
			}
			if a.History != nil && now.Sub(lastPrune) > 6*time.Hour {
				lastPrune = now
				_ = a.History.Prune(now.AddDate(0, 0, -s.HistoryDays), itemIDs(a.effectiveItems(s)))
			}
			a.updateTray()
		}
	}
}

func itemIDs(items []model.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// ---- History & statistics for the UI -----------------------------------------

// TimelineCell is one expected drop in the timeline.
type TimelineCell struct {
	Expected time.Time  `json:"expected"`
	Status   string     `json:"status"` // ok | late | missed | pending | unknown
	DelaySec *int64     `json:"delaySec,omitempty"`
	ModTime  *time.Time `json:"modTime,omitempty"`
}

// Stats summarizes punctuality.
type Stats struct {
	Days        int     `json:"days"`
	Expected    int     `json:"expected"`
	OnTime      int     `json:"onTime"`
	Late        int     `json:"late"`
	Missed      int     `json:"missed"`
	Unknown     int     `json:"unknown"`     // monitor was not running
	Punctuality float64 `json:"punctuality"` // % on time of measured
	AvgDelaySec int64   `json:"avgDelaySec"`
	MaxDelaySec int64   `json:"maxDelaySec"`
}

// HistoryView is returned by getHistory.
type HistoryView struct {
	Stats       Stats              `json:"stats"`
	Timeline    []TimelineCell     `json:"timeline"`
	Arrivals    []store.Arrival    `json:"arrivals"`
	Transitions []store.Transition `json:"transitions"`
	Sizes       []int64            `json:"sizes"` // oldest first
}

func covered(periods []store.Period, t time.Time) bool {
	for _, p := range periods {
		if !t.Before(p.Start) && !t.After(p.End.Add(90*time.Second)) {
			return true
		}
	}
	return false
}

func (a *App) itemHistory(id string, days int) (HistoryView, error) {
	it, ok := a.findItem(id)
	if !ok {
		return HistoryView{}, errors.New("az elem nem található")
	}
	out := HistoryView{Timeline: []TimelineCell{}, Arrivals: []store.Arrival{}, Transitions: []store.Transition{}, Sizes: []int64{}}
	if a.History == nil {
		return out, nil
	}
	if days <= 0 {
		days = 30
	}
	now := time.Now()
	from := now.AddDate(0, 0, -days)
	arr, err := a.History.Arrivals(id, from.AddDate(0, 0, -1), 5000)
	if err != nil {
		return out, err
	}
	if arr != nil {
		out.Arrivals = arr
	}
	if len(out.Arrivals) > 60 {
		out.Arrivals = out.Arrivals[:60]
	}
	if tr, err := a.History.Transitions(id, from, 60); err == nil && tr != nil {
		out.Transitions = tr
	}
	for i := len(arr) - 1; i >= 0 && len(out.Sizes) < 60; i-- {
		out.Sizes = append(out.Sizes, arr[i].Size)
	}
	if len(arr) > 60 {
		out.Sizes = out.Sizes[len(out.Sizes)-60:]
	}

	sc, err := schedule.Compile(it.Schedule, a.Calendar(), a.Loc)
	if err != nil {
		return out, nil
	}
	start := from
	if it.CreatedAt.After(start) {
		start = it.CreatedAt
	}
	exps := sc.Between(start, now, 5000)
	byExp := map[int64]store.Arrival{}
	for _, x := range arr {
		if x.Expected != nil {
			k := x.Expected.UnixMilli()
			if prev, ok := byExp[k]; !ok || x.ModTime.Before(prev.ModTime) {
				byExp[k] = x // the first file that satisfied the expectation
			}
		}
	}
	periods := a.History.UptimeSince(start.Add(-24 * time.Hour))
	grace := it.Grace()
	st := Stats{Days: days}
	var sumDelay int64
	cells := []TimelineCell{}
	for _, t := range exps {
		c := TimelineCell{Expected: t}
		if x, ok := byExp[t.UnixMilli()]; ok {
			d := int64(0)
			if x.DelaySec != nil {
				d = *x.DelaySec
			}
			mt := x.ModTime
			c.DelaySec, c.ModTime = &d, &mt
			if time.Duration(d)*time.Second <= grace {
				c.Status = "ok"
				st.OnTime++
			} else {
				c.Status = "late"
				st.Late++
			}
			if d > 0 {
				sumDelay += d
			}
			if d > st.MaxDelaySec {
				st.MaxDelaySec = d
			}
		} else if now.Before(t.Add(grace)) {
			c.Status = "pending"
		} else if covered(periods, t.Add(grace)) {
			c.Status = "missed"
			st.Missed++
		} else {
			c.Status = "unknown"
			st.Unknown++
		}
		if c.Status != "pending" {
			st.Expected++
		}
		cells = append(cells, c)
	}
	measured := st.OnTime + st.Late + st.Missed
	if measured > 0 {
		st.Punctuality = float64(st.OnTime) * 100 / float64(measured)
	}
	if n := st.OnTime + st.Late; n > 0 {
		st.AvgDelaySec = sumDelay / int64(n)
	}
	if len(cells) > 40 {
		cells = cells[len(cells)-40:]
	}
	out.Stats = st
	out.Timeline = cells
	return out, nil
}

// EventRow is one line of the global event log.
type EventRow struct {
	store.Transition
	ItemName string `json:"itemName"`
	Group    string `json:"group"`
}

func (a *App) eventLog(days int) ([]EventRow, error) {
	if a.History == nil {
		return []EventRow{}, nil
	}
	if days <= 0 {
		days = 7
	}
	tr, err := a.History.Transitions("", time.Now().AddDate(0, 0, -days), 1000)
	if err != nil {
		return nil, err
	}
	names := map[string]model.Item{}
	for _, it := range a.effectiveItems(a.Settings.Get()) {
		names[it.ID] = it
	}
	out := make([]EventRow, 0, len(tr))
	for _, t := range tr {
		it, ok := names[t.ItemID]
		name := it.Name
		if !ok {
			name = "(törölt elem)"
		}
		out = append(out, EventRow{Transition: t, ItemName: name, Group: it.Group})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func (a *App) registerHistoryAPI() {
	type histParam struct {
		ID   string `json:"id"`
		Days int    `json:"days"`
	}
	a.register("getHistory", func(p histParam) (HistoryView, error) { return a.itemHistory(p.ID, p.Days) })
	a.register("eventLog", func(days int) ([]EventRow, error) { return a.eventLog(days) })
	type ackParam struct {
		ID    string `json:"id"`
		Acked bool   `json:"acked"`
	}
	a.register("ackItem", func(p ackParam) error {
		a.Engine.SetAcked(p.ID, p.Acked)
		return nil
	})
	a.register("testNotification", func() error {
		a.P.Notify("Próbaértesítés", "Így fog kinézni egy értesítés. Ha nem látja, ellenőrizze a Windows értesítési beállításait (Fókuszsegéd / Ne zavarjanak).", NotifyApp)
		return nil
	})
}
