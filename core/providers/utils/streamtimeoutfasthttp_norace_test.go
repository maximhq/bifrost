//go:build !race

package utils

import "testing"

func skipUnderRace(_ *testing.T) {}
