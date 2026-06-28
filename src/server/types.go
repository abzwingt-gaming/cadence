// types.go — shared types used across db.go, db_sqlite.go, api.go.

package main

// SongData is a single row returned from a metadata search.
type SongData struct {
	ID     int    `json:"id"`
	Artist string `json:"Artist"`
	Title  string `json:"Title"`
	Album  string `json:"Album"`
	Genre  string `json:"Genre"`
	Year   string `json:"Year"`
}

// dbSearch wraps searchByQuery for use in HTTP handlers.
func dbSearch(q string) ([]SongData, error) {
	return searchByQuery(q)
}
