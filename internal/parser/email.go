package parser

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"net/mail"
	"regexp"
	"strings"
)

type ParsedReply struct {
	FromAddress string `json:"from_address"` // 返信用送信元（元メールの宛先アドレス、自ドメイン等）
	ToAddress   string `json:"to_address"`   // 返信用宛先（元メールの差出人・Reply-Toアドレス）
	ToName      string `json:"to_name"`      // 元メールの差出人名
	Subject     string `json:"subject"`      // 返信用件名（Re: 付き）
	QuotedBody  string `json:"quoted_body"`  // 引用本文（> ...）
}

// ParseEmailSource はGmail等の生メールソース（ヘッダーおよび本文）を解析し、返信に必要な各項目を抽出します。
func ParseEmailSource(rawSource, defaultDomain string) (*ParsedReply, error) {
	// 前後の空白トリム
	trimmed := strings.TrimSpace(rawSource)
	if trimmed == "" {
		return &ParsedReply{}, nil
	}

	reader := bufio.NewReader(strings.NewReader(trimmed))
	msg, err := mail.ReadMessage(reader)

	result := &ParsedReply{}
	wordDecoder := &mime.WordDecoder{}

	if err == nil {
		// 1. 返信先（作成画面の To）の特定: Reply-To を最優先、なければ From
		var targetAddr *mail.Address
		if replyTo := msg.Header.Get("Reply-To"); replyTo != "" {
			if addrs, pErr := mail.ParseAddressList(replyTo); pErr == nil && len(addrs) > 0 {
				targetAddr = addrs[0]
			}
		}
		if targetAddr == nil {
			if from := msg.Header.Get("From"); from != "" {
				if addrs, pErr := mail.ParseAddressList(from); pErr == nil && len(addrs) > 0 {
					targetAddr = addrs[0]
				}
			}
		}

		if targetAddr != nil {
			result.ToAddress = targetAddr.Address
			decodedName, dErr := wordDecoder.DecodeHeader(targetAddr.Name)
			if dErr == nil {
				result.ToName = decodedName
			} else {
				result.ToName = targetAddr.Name
			}
		}

		// 2. 送信元（作成画面の From）の特定: Delivered-To, X-Original-To, To から自ドメインまたは最初のアドレスを取得
		var candidateAddrs []string
		for _, key := range []string{"Delivered-To", "X-Forwarded-To", "X-Original-To", "To"} {
			if val := msg.Header.Get(key); val != "" {
				if addrs, pErr := mail.ParseAddressList(val); pErr == nil {
					for _, a := range addrs {
						candidateAddrs = append(candidateAddrs, a.Address)
					}
				} else {
					// 正規表現でメールアドレスを抽出
					for _, m := range extractEmails(val) {
						candidateAddrs = append(candidateAddrs, m)
					}
				}
			}
		}

		// 候補の中から defaultDomain と一致するものを優先、なければ先頭候補
		for _, addr := range candidateAddrs {
			if defaultDomain != "" && strings.HasSuffix(strings.ToLower(addr), "@"+strings.ToLower(defaultDomain)) {
				result.FromAddress = addr
				break
			}
		}
		if result.FromAddress == "" && len(candidateAddrs) > 0 {
			result.FromAddress = candidateAddrs[0]
		}

		// 3. 件名（Subject）の特定と Re: 付与
		rawSubject := msg.Header.Get("Subject")
		if rawSubject != "" {
			decodedSub, dErr := wordDecoder.DecodeHeader(rawSubject)
			if dErr == nil {
				rawSubject = decodedSub
			}
			rawSubject = strings.TrimSpace(rawSubject)
			if !strings.HasPrefix(strings.ToLower(rawSubject), "re:") {
				result.Subject = "Re: " + rawSubject
			} else {
				result.Subject = rawSubject
			}
		}

		// 4. 本文の引用（Body）
		bodyBytes, _ := io.ReadAll(msg.Body)
		if len(bodyBytes) > 0 {
			result.QuotedBody = formatQuote(string(bodyBytes))
		}
	} else {
		// ヘッダー全体ではない場合（ヘッダー断片や貼り付けデータ）の簡易フォールバック解析
		result = parseLooseEmailSource(trimmed, defaultDomain)
	}

	return result, nil
}

var emailRegex = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

func extractEmails(text string) []string {
	return emailRegex.FindAllString(text, -1)
}

func parseLooseEmailSource(text, defaultDomain string) *ParsedReply {
	res := &ParsedReply{}
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		l := strings.TrimSpace(line)
		lower := strings.ToLower(l)

		if strings.HasPrefix(lower, "from:") {
			addrs := extractEmails(l)
			if len(addrs) > 0 {
				res.ToAddress = addrs[0]
			}
		} else if strings.HasPrefix(lower, "to:") || strings.HasPrefix(lower, "delivered-to:") {
			addrs := extractEmails(l)
			for _, a := range addrs {
				if defaultDomain != "" && strings.HasSuffix(strings.ToLower(a), "@"+strings.ToLower(defaultDomain)) {
					res.FromAddress = a
					break
				}
			}
			if res.FromAddress == "" && len(addrs) > 0 {
				res.FromAddress = addrs[0]
			}
		} else if strings.HasPrefix(lower, "subject:") {
			sub := strings.TrimSpace(l[8:])
			if !strings.HasPrefix(strings.ToLower(sub), "re:") {
				res.Subject = "Re: " + sub
			} else {
				res.Subject = sub
			}
		}
	}

	return res
}

func formatQuote(body string) string {
	lines := strings.Split(body, "\n")
	var buf bytes.Buffer
	buf.WriteString("\n\n--- 元のメッセージ ---\n")
	count := 0
	for _, l := range lines {
		// 最大50行程度に制限して引用枠をスッキリ保つ
		if count > 60 {
			buf.WriteString("> ... (以降省略)\n")
			break
		}
		buf.WriteString("> " + l + "\n")
		count++
	}
	return buf.String()
}
