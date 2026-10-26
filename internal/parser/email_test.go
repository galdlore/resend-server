package parser

import (
	"testing"
)

func TestParseEmailSource(t *testing.T) {
	raw := `Delivered-To: shopping@mydomain.com
Received: by 2002:a05:6512:... with SMTP id ...;
From: "Amazon.co.jp" <order-update@amazon.co.jp>
To: shopping@mydomain.com
Subject: ご注文の確認
Reply-To: no-reply@amazon.co.jp
Message-ID: <order-12345@amazon.co.jp>

お客様のご注文を承りました。
お届け予定日は明日です。
`

	parsed, err := ParseEmailSource(raw, "mydomain.com")
	if err != nil {
		t.Fatalf("パースに失敗しました: %v", err)
	}

	if parsed.ToAddress != "no-reply@amazon.co.jp" {
		t.Errorf("Reply-Toの取得に失敗: got %s, want no-reply@amazon.co.jp", parsed.ToAddress)
	}

	if parsed.FromAddress != "shopping@mydomain.com" {
		t.Errorf("送信元エイリアスの取得に失敗: got %s, want shopping@mydomain.com", parsed.FromAddress)
	}

	if parsed.Subject != "Re: ご注文の確認" {
		t.Errorf("件名の取得に失敗: got %s, want Re: ご注文の確認", parsed.Subject)
	}

	if len(parsed.QuotedBody) == 0 {
		t.Error("引用本文が空です")
	}
}

func TestParseEmailSourceWithoutReplyTo(t *testing.T) {
	raw := `From: "Support Team" <support@service.com>
To: personal@mydomain.com
Subject: Re: 質問の件

ご確認ありがとうございます。
`

	parsed, err := ParseEmailSource(raw, "mydomain.com")
	if err != nil {
		t.Fatalf("パースに失敗しました: %v", err)
	}

	if parsed.ToAddress != "support@service.com" {
		t.Errorf("Fromの取得に失敗: got %s, want support@service.com", parsed.ToAddress)
	}

	if parsed.FromAddress != "personal@mydomain.com" {
		t.Errorf("送信元エイリアスの取得に失敗: got %s, want personal@mydomain.com", parsed.FromAddress)
	}

	if parsed.Subject != "Re: 質問の件" {
		t.Errorf("Re:の重複防止に失敗: got %s, want Re: 質問の件", parsed.Subject)
	}
}
