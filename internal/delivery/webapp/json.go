package webapp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
	"tgtriage/internal/telegram"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleErr writes the appropriate HTTP status for a service-layer error.
func handleErr(w http.ResponseWriter, err error) {
	var (
		apiErr *telegram.APIError
		aiErr  *ai.Error
	)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), domain.ErrInvalidInput.Error()+": "))
	case errors.Is(err, domain.ErrEmptyReply):
		writeError(w, http.StatusBadRequest, "reply text is empty")
	case errors.Is(err, domain.ErrNoConnection):
		writeError(w, http.StatusConflict, "Telegram Business is not connected")
	case errors.Is(err, domain.ErrCannotReply):
		writeError(w, http.StatusConflict, "the bot has no permission to reply in this chat")
	case errors.Is(err, domain.ErrNoSourceChat):
		writeError(w, http.StatusConflict, "the task was created from a forwarded message and has no chat to reply to")
	case errors.Is(err, domain.ErrProviderUnset):
		writeError(w, http.StatusConflict, "не задан ни один API-ключ AI")
	case errors.Is(err, domain.ErrHelpdeskOff):
		writeError(w, http.StatusConflict, "хелпдеск выключен или не указана группа")
	case errors.Is(err, domain.ErrUserBlocked):
		writeError(w, http.StatusConflict, "пользователь заблокировал бота — сообщение не доставлено")
	case errors.Is(err, domain.ErrTopicGone):
		writeError(w, http.StatusConflict, "тема пользователя удалена в группе")
	case errors.Is(err, domain.ErrForbidden):
		writeError(w, http.StatusForbidden, "нет доступа")
	case errors.As(err, &apiErr):
		writeError(w, http.StatusBadGateway, "Telegram: "+apiErr.Description)
	case errors.As(err, &aiErr):
		writeError(w, http.StatusBadGateway, aiErr.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// decodeJSON decodes the request body into v. A genuinely empty body is not an error — several
// endpoints accept an all-optional payload — everything else malformed is a 400.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		return true
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
