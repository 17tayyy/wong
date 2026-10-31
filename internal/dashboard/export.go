package dashboard

import (
	"archive/zip"
	"cmp"
	"net/http"
	"time"
)

// export sends the whole brain as a zip of its files, as the machines keep them; on a sealed brain the server cannot,
// and the page that stands in says where the notes are.
func (d *Dashboard) export(w http.ResponseWriter, r *http.Request, _ string) {
	files, err := d.store.Files()
	if err != nil {
		d.fail(w, err)
		return
	}
	name := "wong-" + cmp.Or(d.account, "brain") + "-" + time.Now().Format("2006-01-02") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	z := zip.NewWriter(w)
	for _, f := range files {
		if f.Deleted {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, f.Modified)
		fw, err := z.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: at})
		if err != nil {
			d.log.Error("export", "err", err)
			return
		}
		if _, err := fw.Write(f.Content); err != nil {
			d.log.Error("export", "err", err)
			return
		}
	}
	if err := z.Close(); err != nil {
		d.log.Error("export", "err", err)
	}
}
