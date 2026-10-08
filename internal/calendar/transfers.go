package calendar

type transfer struct {
	date string
	kind Kind
	name string
}

// transfers holds the yearly bridge days ("munkanap-áthelyezés") published by
// the ministry decree. CHECK EVERY YEAR – the decree is usually published in
// the previous summer/autumn. Users can correct any day in the settings
// (Beállítások → Munkanaptár) without rebuilding.
var transfers = map[int][]transfer{
	2025: {
		{"2025-05-02", KindRestDay, "Áthelyezett pihenőnap"},
		{"2025-05-17", KindWorkday, "Áthelyezett munkanap (máj. 2. helyett)"},
		{"2025-10-24", KindRestDay, "Áthelyezett pihenőnap"},
		{"2025-10-18", KindWorkday, "Áthelyezett munkanap (okt. 24. helyett)"},
		{"2025-12-24", KindRestDay, "Áthelyezett pihenőnap (Szenteste)"},
		{"2025-12-13", KindWorkday, "Áthelyezett munkanap (dec. 24. helyett)"},
	},
	2026: {
		{"2026-01-02", KindRestDay, "Áthelyezett pihenőnap"},
		{"2026-01-10", KindWorkday, "Áthelyezett munkanap (jan. 2. helyett)"},
		{"2026-08-21", KindRestDay, "Áthelyezett pihenőnap"},
		{"2026-08-08", KindWorkday, "Áthelyezett munkanap (aug. 21. helyett)"},
		{"2026-12-24", KindRestDay, "Áthelyezett pihenőnap (Szenteste)"},
		{"2026-12-12", KindWorkday, "Áthelyezett munkanap (dec. 24. helyett)"},
	},
}
