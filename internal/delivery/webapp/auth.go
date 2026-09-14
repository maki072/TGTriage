// Package webapp is the Telegram Mini App delivery layer: a JSON API plus an embedded
// mobile-first single-page frontend, backed by the same service layer as the bot UI.
package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxInitDataAge bounds how long a Mini App launch's initData is trusted for. Telegram
// regenerates it every time the app is opened, so this only guards against a stale, leaked
// initData string being replayed long after the session that produced it.
const maxInitDataAge = 24 * time.Hour

// validateInitData verifies Telegram's Mini App launch payload (see
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app) and
// returns the launching user's Telegram id.
func validateInitData(botToken, initData string) (userID int64, ok bool) {
	values, err := url.ParseQuery(initData)
	if err != nil || len(values) == 0 {
		return 0, false
	}
	receivedHash := values.Get("hash")
	if receivedHash == "" {
		return 0, false
	}
	values.Del("hash")

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+values.Get(k))
	}
	dataCheckString := strings.Join(pairs, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	secretKey := secretMAC.Sum(nil)

	hashMAC := hmac.New(sha256.New, secretKey)
	hashMAC.Write([]byte(dataCheckString))
	computedHash := hex.EncodeToString(hashMAC.Sum(nil))

	if !hmac.Equal([]byte(computedHash), []byte(strings.ToLower(receivedHash))) {
		return 0, false
	}

	if authDateStr := values.Get("auth_date"); authDateStr != "" {
		if sec, err := strconv.ParseInt(authDateStr, 10, 64); err == nil {
			if time.Since(time.Unix(sec, 0)) > maxInitDataAge {
				return 0, false
			}
		}
	}

	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return 0, false
	}
	return user.ID, true
}
