// routes.go

package main

import (
	"net/http"

	"gopkg.in/antage/eventsource.v1"
)

var radiodata_sse = eventsource.New(nil, nil)

func routes() *http.ServeMux {
	r := http.NewServeMux()
	r.Handle("/api/radiodata/sse",       radiodata_sse)
	r.Handle("/api/search",              Search())
	r.Handle("/api/request/id",          rateLimitRequest(RequestID()))
	r.Handle("/api/request/bestmatch",   rateLimitRequest(RequestBestMatch()))
	r.Handle("/api/nowplaying/metadata", NowPlayingMetadata())
	r.Handle("/api/nowplaying/albumart", rateLimitArt(NowPlayingAlbumArt()))
	r.Handle("/api/history",             History())
	r.Handle("/api/listenurl",           ListenURL())
	r.Handle("/api/listeners",           Listeners())
	r.Handle("/api/bitrate",             Bitrate())
	r.Handle("/api/version",             Version())
	r.Handle("/readyz",                  Readyz())
	r.Handle("/healthz",                 Healthz())
	if c.DevMode {
		r.Handle("/api/dev/skip",        DevSkip())
		r.Handle("/api/admin/rescan",    AdminRescan())
	}
	r.Handle("/", http.FileServer(http.Dir(c.RootPath+"./public/")))
	return r
}
