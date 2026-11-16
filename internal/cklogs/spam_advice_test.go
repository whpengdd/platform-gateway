package cklogs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIO042RecordedErrors(t *testing.T) {
	data, err := os.ReadFile("testdata/io042-errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var errors []string
	if err := json.Unmarshal(data, &errors); err != nil {
		t.Fatal(err)
	}
	if len(errors) != 18 {
		t.Fatal("expected all 18 recorded failures")
	}
	for _, raw := range errors {
		if got := friendlyBounceReason(Entry{Errinfo: raw}); got != "收件方反垃圾策略拒收" {
			t.Fatalf("incorrect mapping: %s", got)
		}
	}
}

const io042Error = "(Proxy)Host example.net said 550-Your message to <recipient@example.net> was classified as SPAM. Please add morern550-content, cut down on HTML links, use fewer naughty words etc. Also, askrn550-your IT dept to make sure your mailserver has REVERSEDNS, SPF, DKIM, and isrn550-not on any black lists. Your score: 110. Spam Points:rn550 ReverseDNS_Valid:-10 | Rspamd:+120"

func TestIO042ScopedReason(t *testing.T) {
	for _, raw := range []string{io042Error, strings.ReplaceAll(io042Error, "rn550", "\r\n550"), strings.ReplaceAll(io042Error, "110", "121")} {
		entry := Entry{Errinfo: raw}
		if got := friendlyBounceReason(entry); got != "收件方反垃圾策略拒收" {
			t.Fatalf("unexpected reason: %s", got)
		}
		if entry.Errinfo != raw {
			t.Fatal("raw evidence changed")
		}
	}
}

func TestIO042RepeatedAdvice(t *testing.T) {
	repeated := io042Error + " " + io042Error
	if got := friendlyBounceReason(Entry{Errinfo: repeated}); got != "收件方反垃圾策略拒收" {
		t.Fatalf("repeated advice misclassified: %s", got)
	}
	if got := friendlyBounceReason(Entry{Errinfo: repeated + " IP in blacklist"}); got != "邮件被对方的黑名单策略拒收" {
		t.Fatalf("independent blacklist evidence lost: %s", got)
	}
}

func TestIO042DoesNotChangeOtherDiagnoses(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"550 IP in blacklist", "邮件被对方的黑名单策略拒收"},
		{"550 User in blacklist", "邮件被对方的黑名单策略拒收"},
		{"550 sender domain is blacklisted", "邮件被对方的黑名单策略拒收"},
		{io042Error + " IP in blacklist", "邮件被对方的黑名单策略拒收"},
		{io042Error + " SPF_REJECT", "收件人拒绝接收此邮件"},
		{"550 classified as SPAM", "邮件被对方服务器的安全策略拒收"},
		{"550 Spam message rejected", "邮件被对方服务器的安全策略拒收"},
		{"550 mailbox full", "收件人邮箱空间已满"},
		{"550 user unknown; " + io042Error, "收件人地址不存在，请核对后重试"},
		{"550 SPF_REJECT", "550 SPF_REJECT"},
		{"550 Invalid IP", "550 Invalid IP"},
	}
	for _, tc := range cases {
		if got := friendlyBounceReason(Entry{Errinfo: tc.raw}); got != tc.want {
			t.Errorf("%q => %q; want %q", tc.raw, got, tc.want)
		}
	}
	// Bounce evidence retains its original precedence over Errinfo.
	if got := friendlyBounceReason(Entry{BounceReason: "550 IP in blacklist", Errinfo: io042Error}); got != "邮件被对方的黑名单策略拒收" {
		t.Fatal(got)
	}
}

func TestIO042BothDirectionsAndUnchangedStatus(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		for _, tc := range []struct{ result, status string }{{"4", "failed"}, {"0", "delivered"}, {"1", "pending"}} {
			entry := Entry{Tid: "T1", Cmd: "remote", Result: tc.result, Errinfo: io042Error, Sender: "sender@example.com", Recipients: []string{"recipient@example.net"}}
			rows := aggregatePeerRows([]Entry{entry}, direction, "da")
			if len(rows) != 1 || rows[0].DeliveryStatus != tc.status || rows[0].Errinfo != io042Error {
				t.Fatalf("unexpected aggregation: %+v", rows)
			}
			if tc.status == "failed" && (rows[0].FailureReason != "收件方反垃圾策略拒收" || rows[0].StatusText != rows[0].FailureReason) {
				t.Fatalf("incorrect explanation: %+v", rows[0])
			}
		}
	}
}
