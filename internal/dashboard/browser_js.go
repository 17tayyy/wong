//go:build js

package dashboard

import "net/http"

// browserBundle is false in the browser, which runs the bundle rather than serving it.
func browserBundle() bool { return false }

func (d *Dashboard) browserRoutes(*http.ServeMux) {}
