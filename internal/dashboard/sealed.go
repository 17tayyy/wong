package dashboard

import (
	"net/http"
	"strings"
)

// readsNotes tells the pages that show a brain's notes, which a sealed brain stands in for, from those that work without them.
func readsNotes(path string) bool {
	for _, open := range []string{"/account", "/device", "/webhooks", "/oauth", "/encryption", "/browser"} {
		if path == open || strings.HasPrefix(path, open+"/") {
			return false
		}
	}
	return true
}

// sealedPage stands in for every page that would show the notes of an end-to-end encrypted brain.
func (d *Dashboard) sealedPage(w http.ResponseWriter, r *http.Request, machine string) {
	p, err := d.base(r, "", machine, &brain{})
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title, p.Browser = "Encrypted", browserBundle()
	d.render(w, "sealed", p)
}

// suspendedPage stands in for every page of an account the service suspended, saying why.
func (d *Dashboard) suspendedPage(w http.ResponseWriter, r *http.Request, machine string, why error) {
	p, err := d.base(r, "", machine, &brain{})
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title, p.Error = "Suspended", why.Error()
	w.WriteHeader(http.StatusForbidden)
	d.render(w, "suspended", p)
}
