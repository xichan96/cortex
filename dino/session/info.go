package session

import (
	"time"
)

type Info struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Summary   *Summary   `json:"summary,omitempty"`
	Time      Time       `json:"time"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type Summary struct {
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Files     int    `json:"files"`
	Diffs     string `json:"diffs,omitempty"`
}

type Time struct {
	Created    int64 `json:"created"`
	Updated    int64 `json:"updated"`
	Compacting int64 `json:"compacting,omitempty"`
	Archived   int64 `json:"archived,omitempty"`
}

func NewInfo(id, title string) *Info {
	now := time.Now().UnixMilli()
	return &Info{
		ID:    id,
		Title: title,
		Time: Time{
			Created: now,
			Updated: now,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func (s *Info) Touch() {
	s.Time.Updated = time.Now().UnixMilli()
	s.UpdatedAt = time.Now()
}

func (s *Info) SetSummary(additions, deletions, files int, diffs string) {
	s.Summary = &Summary{
		Additions: additions,
		Deletions: deletions,
		Files:     files,
		Diffs:     diffs,
	}
	s.Touch()
}

func (s *Info) Archive() {
	s.Time.Archived = time.Now().UnixMilli()
	s.Touch()
}

func (s *Info) SetTitle(title string) {
	s.Title = title
	s.Touch()
}
