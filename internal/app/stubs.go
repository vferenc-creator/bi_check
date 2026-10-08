package app

import (
	"time"

	"bimonitor/internal/model"
	"bimonitor/internal/notify"
)

// sendNoticeEmails is implemented in phase 4 (SMTP).
func (a *App) sendNoticeEmails(ns []notify.Notice) {}

// maybeDailySummary is implemented in phase 4.
func (a *App) maybeDailySummary(s model.Settings, now time.Time) {}
