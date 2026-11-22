package api

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

const maxBodyBytes = 1 << 20 // 1 MiB; JSON bodies here are small

// errorBody is the error shape shared with Helpdesk: { "error": { "code", "message" } }.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return errEmptyBody
	}
	// A browser sends a text/plain body across origins without asking first, so only JSON is read.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return errNotJSON
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if err == io.EOF {
			return errEmptyBody
		}
		return err
	}
	return nil
}

var (
	errEmptyBody = jsonError("request body is empty")
	errNotJSON   = jsonError("send the body as JSON, with Content-Type: application/json")
)

type jsonError string

func (e jsonError) Error() string { return string(e) }
