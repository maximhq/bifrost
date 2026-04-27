//go:build race

package utils

import "testing"

func skipUnderRace(t *testing.T) {
	t.Skip("fasthttp's resp.CloseBodyStream races with in-flight Read on its internal *requestStream — fasthttp-internal race, not Bifrost code. See streamtimeoutfasthttp_test.go for full explanation.")
}
