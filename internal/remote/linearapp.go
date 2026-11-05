package remote

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/seal"
)

// LinearApp is the Linear OAuth application a server connects workspaces with, and the secret that signs the
// webhooks Linear sends it from each workspace that let it in.
type LinearApp struct {
	OAuth         linear.OAuth
	WebhookSecret string
}

// Handoff is what the server holds for this machine's next sync: Linear's token, sealed for this machine when the
// brain is sealed, or word that Linear was disconnected and the token is to be revoked and removed.
type Handoff struct {
	Kind   string `json:"kind,omitempty"`
	Blob   []byte `json:"blob,omitempty"`
	Sealed bool   `json:"sealed,omitempty"`
}

// HandOff keeps a token for the brain's machines to take on their next sync. A sealed brain's server keeps it only
// sealed for each device of the account, so it cannot read it back; one in the clear keeps it as it is.
func (s *Store) HandOff(token []byte) error {
	sealed, err := s.Sealed()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM handoffs`); err != nil {
		return err
	}
	now := utcNow()
	if !sealed {
		if _, err := tx.Exec(`INSERT INTO handoffs (device, kind, blob, created_at) VALUES ('', 'token', ?, ?)`, token, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	rows, err := tx.Query(`SELECT device, public FROM keyring`)
	if err != nil {
		return err
	}
	type device struct {
		name   string
		public []byte
	}
	var devices []device
	for rows.Next() {
		var d device
		if err := rows.Scan(&d.name, &d.public); err != nil {
			rows.Close()
			return err
		}
		devices = append(devices, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range devices {
		blob, err := seal.SealFor(d.public, token)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO handoffs (device, kind, blob, created_at) VALUES (?, 'token', ?, ?)`, d.name, blob, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HandOffForget leaves word for the brain's machines that Linear was disconnected, in place of any token.
func (s *Store) HandOffForget() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM handoffs`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO handoffs (device, kind, created_at) VALUES ('', 'forget', ?)`, utcNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// Handoff returns what the server holds for a machine: what was sealed for it, or what is for every machine.
func (s *Store) Handoff(device string) (Handoff, error) {
	var h Handoff
	var who string
	err := s.db.QueryRow(`SELECT device, kind, blob FROM handoffs WHERE device = ? OR device = '' ORDER BY device DESC LIMIT 1`, device).Scan(&who, &h.Kind, &h.Blob)
	if errors.Is(err, sql.ErrNoRows) {
		return Handoff{}, nil
	}
	h.Sealed = who != ""
	return h, err
}

// DropHandoffs forgets what the server held for the machines, once one of them took it into the brain.
func (s *Store) DropHandoffs() error {
	_, err := s.db.Exec(`DELETE FROM handoffs`)
	return err
}

// Handoff asks the server what it holds for this machine; a server too old to hold anything holds nothing.
func (c *Client) Handoff(ctx context.Context) (Handoff, error) {
	var h Handoff
	resp, err := c.send(ctx, http.MethodGet, "/api/handoff", nil)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return h, json.NewDecoder(resp.Body).Decode(&h)
	case http.StatusNotFound:
		return h, nil
	}
	return h, failed(resp)
}

// TakeHandoff brings into the brain what the server holds for this machine: Linear's token, saved where every
// machine finds it once the brain syncs, or, once Linear was disconnected, the token revoked and removed.
func TakeHandoff(ctx context.Context, c *Client, brain string) error {
	h, err := c.Handoff(ctx)
	if err != nil || h.Kind == "" {
		return err
	}
	switch h.Kind {
	case "token":
		data := h.Blob
		if h.Sealed {
			if c.Device == nil {
				return errors.New("Linear's token is sealed for this machine, which has no key to open it")
			}
			if data, err = c.Device.OpenSealed(h.Blob); err != nil {
				return err
			}
		}
		var t linear.Token
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		err = linear.SaveToken(brain, t)
	case "forget":
		err = c.Linear.Forget(ctx, brain)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/api/handoff", nil, &struct{}{})
}

// linearAppHook takes the webhooks of the Linear application, from every workspace that let it in, for the brains
// linked to that workspace: issues that close leave tasks as a workspace's own webhook does, and a workspace that
// revokes the application is unlinked, its brains' machines told to remove the token.
func linearAppHook(brains Brains, hooks Webhooks, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := signedBody(w, r, log, hooks.Linear.WebhookSecret, "Linear-Signature", "")
		if !ok {
			return
		}
		var ev linearEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !freshLinear(w, log, ev) {
			return
		}
		accounts := hooks.Links.Accounts("linear", ev.OrganizationID)
		if len(accounts) == 0 {
			ignore(w, log, "linear app", "workspace "+ev.OrganizationID+" is linked to no brain")
			return
		}
		revoked := ev.Type == "OAuthApp" && ev.Action == "revoked"
		total, failed := 0, false
		for _, account := range accounts {
			store, err := storeOf(brains, account)
			if err == nil && revoked {
				err = store.HandOffForget()
			} else if err == nil {
				var n int
				n, _, err = takeIssue(store, ev, log)
				total += n
			}
			if err != nil {
				log.Error("linear app", "account", account, "err", err)
				failed = true
			}
		}
		if revoked {
			if err := hooks.Links.Forget("linear", ev.OrganizationID); err != nil {
				log.Error("linear app", "err", err)
				failed = true
			}
		}
		if failed {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, hookResponse{Tasks: total})
	}
}

// ConnectLinear finishes a person's Connect Linear: it trades the code Linear sent back for a token, links the
// workspace to the account in place of any other, and hands the token to the brain's machines.
func ConnectLinear(ctx context.Context, app *LinearApp, links *Links, store *Store, account, code, redirect, verifier string) error {
	t, err := app.OAuth.Exchange(ctx, code, redirect, verifier)
	if err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := store.HandOff(data); err != nil {
		return err
	}
	for _, old := range links.Of("linear", account) {
		if err := links.Remove("linear", old.ID, account); err != nil {
			return err
		}
	}
	return links.Add(Link{Kind: "linear", ID: t.Organization, Account: account, Name: t.Name})
}
