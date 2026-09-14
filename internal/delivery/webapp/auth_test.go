package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signInitData builds a valid Telegram Mini App initData string for tests, mirroring the
// client-side algorithm this package's validateInitData must accept.
func signInitData(t *testing.T, botToken string, userID int64, authDate time.Time) string {
	t.Helper()
	v := url.Values{}
	v.Set("query_id", "AAEXAMPLE")
	v.Set("user", `{"id":`+strconv.FormatInt(userID, 10)+`,"first_name":"Test"}`)
	v.Set("auth_date", strconv.FormatInt(authDate.Unix(), 10))

	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	// deterministic order matters for the test data only; validateInitData sorts independently.
	pairs := []string{"auth_date=" + v.Get("auth_date"), "query_id=" + v.Get("query_id"), "user=" + v.Get("user")}
	dataCheckString := strings.Join(pairs, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	secretKey := secretMAC.Sum(nil)

	hashMAC := hmac.New(sha256.New, secretKey)
	hashMAC.Write([]byte(dataCheckString))
	v.Set("hash", hex.EncodeToString(hashMAC.Sum(nil)))

	return v.Encode()
}

func TestValidateInitDataAccepted(t *testing.T) {
	const token = "123:ABC-test-token"
	initData := signInitData(t, token, 802675642, time.Now())
	userID, ok := validateInitData(token, initData)
	if !ok || userID != 802675642 {
		t.Fatalf("expected valid init data for user 802675642, got ok=%v userID=%d", ok, userID)
	}
}

func TestValidateInitDataWrongToken(t *testing.T) {
	initData := signInitData(t, "123:ABC-test-token", 802675642, time.Now())
	if _, ok := validateInitData("999:DIFFERENT-token", initData); ok {
		t.Fatal("init data signed with a different bot token must not validate")
	}
}

func TestValidateInitDataTampered(t *testing.T) {
	const token = "123:ABC-test-token"
	initData := signInitData(t, token, 802675642, time.Now())
	tampered := strings.Replace(initData, "802675642", "999999999", 1)
	if _, ok := validateInitData(token, tampered); ok {
		t.Fatal("tampering with a signed field must invalidate the hash")
	}
}

func TestValidateInitDataStale(t *testing.T) {
	const token = "123:ABC-test-token"
	initData := signInitData(t, token, 802675642, time.Now().Add(-48*time.Hour))
	if _, ok := validateInitData(token, initData); ok {
		t.Fatal("init data older than maxInitDataAge must be rejected")
	}
}

func TestValidateInitDataMalformed(t *testing.T) {
	for _, bad := range []string{"", "not a query string with hash", "hash=deadbeef"} {
		if _, ok := validateInitData("token", bad); ok {
			t.Errorf("expected rejection for %q", bad)
		}
	}
}
