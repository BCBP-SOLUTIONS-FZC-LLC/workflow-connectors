package sendemail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// containing "\r\n" could otherwise inject extra headers into the message.
func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

func buildRawMIME(msg EmailMessage) ([]byte, error) {
	var buf bytes.Buffer

	senderName := stripCRLF(msg.SenderName)
	receiverName := stripCRLF(msg.ReceiverName)
	subject := stripCRLF(msg.Subject)
	senderEmail := stripCRLF(msg.SenderEmail)
	receiverEmail := stripCRLF(msg.ReceiverEmail)

	from := senderEmail
	if senderName != "" {
		from = mime.QEncoding.Encode("utf-8", senderName) + " <" + senderEmail + ">"
	}
	to := receiverEmail
	if receiverName != "" {
		to = mime.QEncoding.Encode("utf-8", receiverName) + " <" + receiverEmail + ">"
	}

	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")

	bodyContentType := stripCRLF(msg.ContentType)
	if bodyContentType == "" {
		bodyContentType = "text/plain"
	}

	if len(msg.Attachments) == 0 {
		fmt.Fprintf(&buf, "Content-Type: %s; charset=UTF-8\r\n\r\n", bodyContentType)
		buf.WriteString(msg.Body)
		return buf.Bytes(), nil
	}

	writer := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", writer.Boundary())

	bodyPart, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type": {bodyContentType + "; charset=UTF-8"},
	})
	if err != nil {
		return nil, fmt.Errorf("mime: create body part: %w", err)
	}
	if _, err := bodyPart.Write([]byte(msg.Body)); err != nil {
		return nil, fmt.Errorf("mime: write body: %w", err)
	}

	for _, a := range msg.Attachments {
		contentType := a.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		attachmentPart, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {contentType},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {fmt.Sprintf("attachment; filename=%q", a.Filename)},
		})
		if err != nil {
			return nil, fmt.Errorf("mime: create attachment part: %w", err)
		}
		encoded := base64.StdEncoding.EncodeToString(a.Content)
		if _, err := attachmentPart.Write([]byte(encoded)); err != nil {
			return nil, fmt.Errorf("mime: write attachment: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("mime: close writer: %w", err)
	}
	return buf.Bytes(), nil
}
