package client

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", Unavailable(), true},
		{"deadline", connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), true},
		{"rate limited", RateLimited("1"), true},
		{"other exhausted", connect.NewError(connect.CodeResourceExhausted, errors.New("x")), false},
		{"not member", NotMember(), false},
		{"plain", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Transient(c.err); got != c.want {
				t.Fatalf("Transient = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	d, ok := RetryAfter(RateLimited("7"), now)
	if !ok || d != 7*time.Second {
		t.Fatalf("seconds form: %v %v", d, ok)
	}
	d, ok = RetryAfter(RateLimited(now.Add(90*time.Second).Format(http.TimeFormat)), now)
	if !ok || d != 90*time.Second {
		t.Fatalf("date form: %v %v", d, ok)
	}
	d, ok = RetryAfter(RateLimited(now.Add(-time.Minute).Format(http.TimeFormat)), now)
	if !ok || d != 0 {
		t.Fatalf("past date: %v %v", d, ok)
	}
	if _, ok := RetryAfter(RateLimited("soon"), now); ok {
		t.Fatal("garbage header must be ignored")
	}
	if _, ok := RetryAfter(Unavailable(), now); ok {
		t.Fatal("no header means no Retry-After")
	}
}

func TestDescribeUsesReason(t *testing.T) {
	got := Describe(NotMember())
	if got == nil || got.Error() != "you are not a member of that conversation" {
		t.Fatalf("Describe = %v", got)
	}
}

func TestDescribeEmptyMessageFallsBackToCode(t *testing.T) {
	for name, err := range map[string]error{
		"nil cause":   connect.NewError(connect.CodeUnavailable, nil),
		"empty cause": connect.NewError(connect.CodeUnavailable, errors.New("")),
	} {
		t.Run(name, func(t *testing.T) {
			if got := Describe(err); got == nil || got.Error() != "unavailable" {
				t.Fatalf("Describe = %q, want %q", got, "unavailable")
			}
		})
	}
}

func TestDescribeKeepsOriginalClassifiable(t *testing.T) {
	err := Describe(RateLimited("1"))
	if !Transient(err) || Reason(err) == 0 {
		t.Fatalf("Describe hid the original error: transient=%v reason=%v", Transient(err), Reason(err))
	}
	if Transient(Describe(NotMember())) {
		t.Fatal("permanent error became transient")
	}
}
