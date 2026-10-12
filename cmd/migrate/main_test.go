package main

import "testing"

func TestPgx5Scheme(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		err  bool
	}{
		{in: "postgres://u:p@h:1/db?sslmode=disable", want: "pgx5://u:p@h:1/db?sslmode=disable"},
		{in: "postgresql://u:p@h:1/db", want: "pgx5://u:p@h:1/db"},
		{in: "pgx5://u:p@h:1/db", want: "pgx5://u:p@h:1/db"},
		{in: "sqlite://x", err: true},
		{in: "", err: true},
	} {
		got, err := pgx5Scheme(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("%q: err=%v want err=%v", tc.in, err, tc.err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestPositive(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		err  bool
	}{
		{in: "1", want: 1},
		{in: "42", want: 42},
		{in: "0", err: true},
		{in: "-1", err: true},
		{in: "x", err: true},
		{in: "", err: true},
	} {
		got, err := positive("up", tc.in)
		if (err != nil) != tc.err {
			t.Errorf("%q: err=%v want err=%v", tc.in, err, tc.err)
			continue
		}
		if err == nil && got != tc.want {
			t.Errorf("%q: got %d want %d", tc.in, got, tc.want)
		}
	}
}
