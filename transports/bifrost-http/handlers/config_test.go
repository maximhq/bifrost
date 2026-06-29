package handlers

import (
	"reflect"
	"testing"
)

func TestGetPasswordPolicyFailures(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     []string
	}{
		{
			name:     "valid password",
			password: "StrongPass1!",
			want:     []string{},
		},
		{
			name:     "missing all requirements",
			password: "",
			want: []string{
				"at least 12 characters",
				"one uppercase letter",
				"one lowercase letter",
				"one number",
				"one special character",
			},
		},
		{
			name:     "missing character classes",
			password: "weakpassword",
			want: []string{
				"one uppercase letter",
				"one number",
				"one special character",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPasswordPolicyFailures(tt.password)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("getPasswordPolicyFailures() = %v, want %v", got, tt.want)
			}
		})
	}
}
