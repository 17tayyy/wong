package remote

import (
	"encoding/json"
	"time"
)

// eventsKept is how long a sealed brain keeps the webhook events its machines have not all fetched.
const eventsKept = 90 * 24 * time.Hour

// Event is a webhook event a sealed brain keeps for its machines: the server cannot read the notes to apply it, so
// each machine applies it to its own copy. Remote is the GitHub repo it came from, which the machine maps to a project.
type Event struct {
	Seq     int64           `json:"seq"`
	Source  string          `json:"source"`
	Remote  string          `json:"remote,omitempty"`
	Event   json.RawMessage `json:"event"`
	Created string          `json:"created"`
}

// keepEvent stores a webhook event for the machines of a sealed brain, and drops the ones older than eventsKept.
func (s *Store) keepEvent(ev event, source, remote string) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := s.db.Exec(`DELETE FROM events WHERE created_at < ?`, now.Add(-eventsKept).Format(time.RFC3339)); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO events (source, remote, event, created_at) VALUES (?, ?, ?, ?)`, source, remote, string(raw), now.Format(time.RFC3339))
	return err
}

// Events returns the webhook events kept after since, and the cursor to ask from next time.
func (s *Store) Events(since int64) ([]Event, int64, error) {
	rows, err := s.db.Query(`SELECT seq, source, remote, event, created_at FROM events WHERE seq > ? ORDER BY seq`, since)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events := []Event{}
	cursor := since
	for rows.Next() {
		var e Event
		var raw string
		if err := rows.Scan(&e.Seq, &e.Source, &e.Remote, &raw, &e.Created); err != nil {
			return nil, 0, err
		}
		e.Event = json.RawMessage(raw)
		events = append(events, e)
		cursor = e.Seq
	}
	return events, cursor, rows.Err()
}
