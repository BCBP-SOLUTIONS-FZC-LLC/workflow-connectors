package gmail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/workflow-connectors/v2/pkg/connectors/sendemail"
)

func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// buildRawMIME renders msg as an RFC 5322 message. Every write goes to a
// bytes.Buffer, whose Write never fails, so the multipart and
// quoted-printable writers it feeds cannot fail either: their errors are
// discarded.
func buildRawMIME(msg sendemail.EmailMessage) []byte {
	var buf bytes.Buffer

	subject := stripCRLF(msg.Subject)

	// mail.Address quotes and RFC 2047-encodes the display name, so a name
	// holding ",", "<", ">", a quote or non-ASCII text cannot add a recipient
	// or change the address (the addresses are validated single addresses).
	fmt.Fprintf(&buf, "From: %s\r\n", addressHeader(msg.SenderName, msg.SenderEmail))
	fmt.Fprintf(&buf, "To: %s\r\n", addressHeader(msg.ReceiverName, msg.ReceiverEmail))
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")

	bodyContentType := stripCRLF(msg.ContentType)
	if bodyContentType == "" {
		bodyContentType = "text/plain"
	}

	if len(msg.Attachments) == 0 {
		fmt.Fprintf(&buf, "Content-Type: %s; charset=UTF-8\r\n", bodyContentType)
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		_ = writeQuotedPrintable(&buf, msg.Body)
		return buf.Bytes()
	}

	writer := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", writer.Boundary())

	bodyPart, _ := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {bodyContentType + "; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	_ = writeQuotedPrintable(bodyPart, msg.Body)

	for _, a := range msg.Attachments {
		contentType := a.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		attachmentPart, _ := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {contentType},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {fmt.Sprintf("attachment; filename=%q", a.Filename)},
		})
		_, _ = attachmentPart.Write(wrapBase64(a.Content))
	}

	_ = writer.Close()
	return buf.Bytes()
}

// addressHeader renders one address-header value: the bare address, or
// mail.Address's quoted, RFC 2047-encoded display name with it.
func addressHeader(name, address string) string {
	name, address = stripCRLF(name), stripCRLF(address)
	if name == "" {
		return address
	}
	return (&mail.Address{Name: name, Address: address}).String()
}

// writeQuotedPrintable encodes body so non-ASCII text and long lines stay
// within RFC 5322's 998-octet line limit.
func writeQuotedPrintable(w io.Writer, body string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(body)); err != nil {
		return err
	}
	return qp.Close()
}

// wrapBase64 encodes content as base64 in CRLF-terminated lines of at most
// 76 characters (RFC 2045 §6.8).
func wrapBase64(content []byte) []byte {
	const lineLen = 76
	encoded := base64.StdEncoding.EncodeToString(content)
	var out bytes.Buffer
	out.Grow(len(encoded) + 2*(len(encoded)/lineLen+1))
	for len(encoded) > lineLen {
		out.WriteString(encoded[:lineLen])
		out.WriteString("\r\n")
		encoded = encoded[lineLen:]
	}
	out.WriteString(encoded)
	return out.Bytes()
}
